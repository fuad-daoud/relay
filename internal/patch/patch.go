// Package patch is a small unified-diff reader sized for relevo's own
// captured patches (relevo show --diff): it parses the "diff --git" / "---" /
// "+++" / "@@ ... @@" structure relevo's capture path already produces and
// numbers each line by its post-image (new-file) line number, so a review
// comment can anchor to a path:line the way `relevo show --diff --anchors` prints
// it.
package patch

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Patch is a parsed unified diff: one File per "diff --git" block, in the
// order they appeared.
type Patch struct {
	Files []File
}

// File is one file's hunks, keyed by its post-image path (the "b/" side of
// "diff --git", overridden by a "+++ b/<path>" line when present).
type File struct {
	Path  string
	Hunks []Hunk
}

// Hunk is one "@@ ... @@" block.
type Hunk struct {
	OldStart int
	OldLines int
	NewStart int
	NewLines int
	Header   string // the "@@ ... @@" line, verbatim
	Lines    []Line
}

// Line is one line inside a hunk.
type Line struct {
	Kind byte   // ' ', '+', or '-'
	Text string // the line's text, without the leading Kind byte
	New  int    // post-image line number; 0 for a '-' line
}

// HasLine reports whether path has a ' ' or '+' line at post-image line
// number line, and returns its hunk and line.
func (p Patch) HasLine(path string, line int) (Hunk, Line, bool) {
	for _, f := range p.Files {
		if f.Path != path {
			continue
		}
		for _, h := range f.Hunks {
			for _, l := range h.Lines {
				if (l.Kind == ' ' || l.Kind == '+') && l.New == line {
					return h, l, true
				}
			}
		}
	}
	return Hunk{}, Line{}, false
}

var hunkHeaderRE = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@(.*)$`)

// rawFile and rawHunk carry everything Parse and Annotate both need,
// including the raw file-header lines ("diff --git", "---", "+++") that
// Annotate copies unchanged and Parse discards.
type rawFile struct {
	headerLines []string
	path        string
	hunks       []rawHunk
}

type rawHunk struct {
	headerLine string
	oldStart   int
	oldLines   int
	newStart   int
	newLines   int
	nextNew    int
	lines      []Line
}

func extractGitDiffPath(line string) string {
	const marker = " b/"
	if idx := strings.LastIndex(line, marker); idx != -1 {
		return line[idx+len(marker):]
	}
	return strings.TrimPrefix(line, "diff --git ")
}

// parseState is the parse in progress across the line-dispatch methods below;
// each one advances it by one line.
type parseState struct {
	files         []rawFile
	cur           *rawFile
	curHunk       *rawHunk
	hunkStartLine int
	inHunk        bool
}

// consume advances the parse by one line, dispatching on the same four
// prefixes parseRaw's caller sees: a new file, a header line, a hunk header,
// or a hunk content line. Anything else -- text outside a "diff --git" block,
// "\ No newline at end of file" -- is ignored.
func (st *parseState) consume(line string, lineNum int) error {
	if strings.HasPrefix(line, "diff --git ") {
		return st.startFile(line)
	}
	if st.cur == nil {
		return nil
	}
	if !st.inHunk && strings.HasPrefix(line, "--- ") {
		st.cur.headerLines = append(st.cur.headerLines, line)
		return nil
	}
	if !st.inHunk && strings.HasPrefix(line, "+++ ") {
		st.addPlusPlus(line)
		return nil
	}
	if m := hunkHeaderRE.FindStringSubmatch(line); m != nil {
		return st.startHunk(m, line, lineNum)
	}
	if st.inHunk && len(line) > 0 && (line[0] == ' ' || line[0] == '+' || line[0] == '-') {
		st.addContentLine(line)
	}
	return nil
}

// startFile flushes any in-progress hunk and file, then begins a new file at
// line's "diff --git" header.
func (st *parseState) startFile(line string) error {
	if err := st.flushHunk(); err != nil {
		return err
	}
	st.flushFile()
	st.cur = &rawFile{path: extractGitDiffPath(line), headerLines: []string{line}}
	st.curHunk = nil
	st.inHunk = false
	return nil
}

// addPlusPlus records a "+++" header line and takes its path as the file's
// path: it overrides the "diff --git" guess, since a rename can disagree.
func (st *parseState) addPlusPlus(line string) {
	st.cur.headerLines = append(st.cur.headerLines, line)
	rest := strings.TrimPrefix(line, "+++ ")
	st.cur.path = strings.TrimPrefix(rest, "b/")
}

// startHunk flushes the previous hunk and begins a new one from a
// "@@ ... @@" header's regex match.
func (st *parseState) startHunk(m []string, line string, lineNum int) error {
	if err := st.flushHunk(); err != nil {
		return err
	}
	oldStart, _ := strconv.Atoi(m[1])
	oldLines := 1
	if m[2] != "" {
		oldLines, _ = strconv.Atoi(m[2])
	}
	newStart, _ := strconv.Atoi(m[3])
	newLines := 1
	if m[4] != "" {
		newLines, _ = strconv.Atoi(m[4])
	}
	st.cur.hunks = append(st.cur.hunks, rawHunk{
		headerLine: line,
		oldStart:   oldStart,
		oldLines:   oldLines,
		newStart:   newStart,
		newLines:   newLines,
		nextNew:    newStart,
	})
	st.curHunk = &st.cur.hunks[len(st.cur.hunks)-1]
	st.hunkStartLine = lineNum
	st.inHunk = true
	return nil
}

// addContentLine appends one ' '/'+'/'-' hunk line, numbering it by the
// hunk's running post-image line count.
func (st *parseState) addContentLine(line string) {
	kind := line[0]
	text := line[1:]
	newVal := 0
	if kind != '-' {
		newVal = st.curHunk.nextNew
		st.curHunk.nextNew++
	}
	st.curHunk.lines = append(st.curHunk.lines, Line{Kind: kind, Text: text, New: newVal})
}

// flushHunk checks the just-finished hunk's declared new-line count against
// what was actually collected; the hunk itself already lives in cur.hunks.
// Called before starting the next hunk or file, and once more after the last
// line.
func (st *parseState) flushHunk() error {
	if st.curHunk == nil {
		return nil
	}
	found := 0
	for _, l := range st.curHunk.lines {
		if l.Kind == ' ' || l.Kind == '+' {
			found++
		}
	}
	if found != st.curHunk.newLines {
		return fmt.Errorf("hunk at %s line %d: expected %d new lines, found %d", st.cur.path, st.hunkStartLine, st.curHunk.newLines, found)
	}
	return nil
}

// flushFile appends the in-progress file to files, if there is one.
func (st *parseState) flushFile() {
	if st.cur != nil {
		st.files = append(st.files, *st.cur)
	}
}

func parseRaw(b []byte) ([]rawFile, error) {
	lines := strings.Split(string(b), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	var st parseState
	for i, line := range lines {
		if err := st.consume(line, i+1); err != nil {
			return nil, err
		}
	}
	if err := st.flushHunk(); err != nil {
		return nil, err
	}
	st.flushFile()

	return st.files, nil
}

// Parse parses a captured unified diff.
func Parse(b []byte) (Patch, error) {
	raws, err := parseRaw(b)
	if err != nil {
		return Patch{}, err
	}
	var p Patch
	for _, rf := range raws {
		f := File{Path: rf.path}
		for _, rh := range rf.hunks {
			f.Hunks = append(f.Hunks, Hunk{
				OldStart: rh.oldStart,
				OldLines: rh.oldLines,
				NewStart: rh.newStart,
				NewLines: rh.newLines,
				Header:   rh.headerLine,
				Lines:    rh.lines,
			})
		}
		p.Files = append(p.Files, f)
	}
	return p, nil
}
