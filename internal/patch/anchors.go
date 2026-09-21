package patch

import (
	"bytes"
	"strconv"
	"strings"
)

// Annotate renders b the way `relay diff --anchors` prints it: each hunk's
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
		for _, hl := range rf.headerLines {
			buf.WriteString(hl)
			buf.WriteByte('\n')
		}

		dashGutter := rf.path + ":-"
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
		pad := func(g string) string {
			if n := maxWidth - len(g); n > 0 {
				return g + strings.Repeat(" ", n)
			}
			return g
		}

		for _, rh := range rf.hunks {
			buf.WriteString(pad(rf.path + ":" + strconv.Itoa(rh.newStart)))
			buf.WriteString("  ")
			buf.WriteString(rh.headerLine)
			buf.WriteByte('\n')

			for _, l := range rh.lines {
				if l.Kind == '-' {
					buf.WriteString(pad(dashGutter))
					buf.WriteString("      ")
				} else {
					buf.WriteString(pad(rf.path + ":" + strconv.Itoa(l.New)))
					buf.WriteString(" ")
				}
				buf.WriteByte(l.Kind)
				buf.WriteString(l.Text)
				buf.WriteByte('\n')
			}
		}
	}

	return buf.Bytes(), nil
}
