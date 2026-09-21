// Package patch is a small unified-diff reader sized for relay's own
// captured patches (relay diff): it parses the "diff --git" / "---" /
// "+++" / "@@ ... @@" structure relay's capture path already produces and
// numbers each line by its post-image (new-file) line number, so a review
// comment can anchor to a path:line the way `relay diff --anchors` prints
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

func parseRaw(b []byte) ([]rawFile, error) {
	lines := strings.Split(string(b), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	var files []rawFile
	var cur *rawFile
	var curHunk *rawHunk
	var hunkStartLine int
	inHunk := false

	flushHunk := func() error {
		if curHunk == nil {
			return nil
		}
		found := 0
		for _, l := range curHunk.lines {
			if l.Kind == ' ' || l.Kind == '+' {
				found++
			}
		}
		if found != curHunk.newLines {
			return fmt.Errorf("hunk at %s line %d: expected %d new lines, found %d", cur.path, hunkStartLine, curHunk.newLines, found)
		}
		return nil
	}
	flushFile := func() {
		if cur != nil {
			files = append(files, *cur)
		}
	}

	for i, line := range lines {
		lineNum := i + 1

		if strings.HasPrefix(line, "diff --git ") {
			if err := flushHunk(); err != nil {
				return nil, err
			}
			flushFile()
			cur = &rawFile{path: extractGitDiffPath(line), headerLines: []string{line}}
			curHunk = nil
			inHunk = false
			continue
		}

		if cur == nil {
			// Outside any "diff --git" block: ignored.
			continue
		}

		if !inHunk && strings.HasPrefix(line, "--- ") {
			cur.headerLines = append(cur.headerLines, line)
			continue
		}

		if !inHunk && strings.HasPrefix(line, "+++ ") {
			cur.headerLines = append(cur.headerLines, line)
			rest := strings.TrimPrefix(line, "+++ ")
			rest = strings.TrimPrefix(rest, "b/")
			cur.path = rest
			continue
		}

		if m := hunkHeaderRE.FindStringSubmatch(line); m != nil {
			if err := flushHunk(); err != nil {
				return nil, err
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
			cur.hunks = append(cur.hunks, rawHunk{
				headerLine: line,
				oldStart:   oldStart,
				oldLines:   oldLines,
				newStart:   newStart,
				newLines:   newLines,
				nextNew:    newStart,
			})
			curHunk = &cur.hunks[len(cur.hunks)-1]
			hunkStartLine = lineNum
			inHunk = true
			continue
		}

		if inHunk && len(line) > 0 && (line[0] == ' ' || line[0] == '+' || line[0] == '-') {
			kind := line[0]
			text := line[1:]
			newVal := 0
			if kind != '-' {
				newVal = curHunk.nextNew
				curHunk.nextNew++
			}
			curHunk.lines = append(curHunk.lines, Line{Kind: kind, Text: text, New: newVal})
			continue
		}

		// "\ No newline at end of file" and any other unrecognised line:
		// ignored.
	}

	if err := flushHunk(); err != nil {
		return nil, err
	}
	flushFile()

	return files, nil
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
