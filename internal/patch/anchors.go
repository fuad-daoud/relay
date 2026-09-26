package patch

import (
	"bytes"
	"strconv"
	"strings"
)

// Annotate renders b the way `relevo show --diff --anchors` prints it: each hunk's
// header gets a "<path>:<line>" gutter naming its post-image start, and
// each ' '/'+' line gets a gutter naming its own post-image line number so
// a reviewer can quote "path:line: comment" straight off the printed diff.
// A '-' line (no post-image line) gets a "<path>:-" gutter instead. File
// headers ("diff --git", "---", "+++") are copied unchanged.
func Annotate(b []byte) ([]byte, error) {
	raws, err := parseRaw(b)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	for _, rf := range raws {
		annotateFile(&buf, rf)
	}
	return buf.Bytes(), nil
}

// annotateFile writes one file's header lines, then its hunks with their
// gutters padded to a common width.
func annotateFile(buf *bytes.Buffer, rf rawFile) {
	for _, hl := range rf.headerLines {
		buf.WriteString(hl)
		buf.WriteByte('\n')
	}

	dashGutter := rf.path + ":-"
	pad := gutterPadder(rf, dashGutter)

	for _, rh := range rf.hunks {
		buf.WriteString(pad(rf.path + ":" + strconv.Itoa(rh.newStart)))
		buf.WriteString("  ")
		buf.WriteString(rh.headerLine)
		buf.WriteByte('\n')

		for _, l := range rh.lines {
			writeAnnotatedLine(buf, rf.path, dashGutter, pad, l)
		}
	}
}

// writeAnnotatedLine writes one hunk line with its gutter: a '-' line gets
// the file's dash gutter and six spaces; any other line gets its own
// "path:line" gutter and one space.
func writeAnnotatedLine(buf *bytes.Buffer, path, dashGutter string, pad func(string) string, l Line) {
	if l.Kind == '-' {
		buf.WriteString(pad(dashGutter))
		buf.WriteString("      ")
	} else {
		buf.WriteString(pad(path + ":" + strconv.Itoa(l.New)))
		buf.WriteString(" ")
	}
	buf.WriteByte(l.Kind)
	buf.WriteString(l.Text)
	buf.WriteByte('\n')
}

// gutterPadder returns a function that right-pads a gutter string with
// spaces to the width of the widest gutter rf's hunks and lines produce, so
// every gutter in one file lines up.
func gutterPadder(rf rawFile, dashGutter string) func(string) string {
	maxWidth := 0
	for _, rh := range rf.hunks {
		if w := len(rf.path + ":" + strconv.Itoa(rh.newStart)); w > maxWidth {
			maxWidth = w
		}
		for _, l := range rh.lines {
			w := len(dashGutter)
			if l.Kind != '-' {
				w = len(rf.path + ":" + strconv.Itoa(l.New))
			}
			if w > maxWidth {
				maxWidth = w
			}
		}
	}
	return func(g string) string {
		if n := maxWidth - len(g); n > 0 {
			return g + strings.Repeat(" ", n)
		}
		return g
	}
}
