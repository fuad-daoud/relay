// Package agentsrc parses, validates and formats relevo's single-source agent
// format, and renders one native file per harness kind from it.
//
// A custom agent is one source file: frontmatter (name, description, shape,
// output, requires, kinds) plus a prompt body. The shipped agents under
// internal/harness/agents are not rendered by this package; they stay
// hand-maintained per kind (spec §3.2).
package agentsrc

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/fuad-daoud/relevo/internal/harness"
)

// ErrBadSource is wrapped by every Parse, Validate and Render error.
var ErrBadSource = errors.New("bad agent source")

// Shape distinguishes an agent that may change the tree from one that may
// write only its artifact directory. It does not change the rendered tools.
type Shape string

const (
	// ShapeWriter is an agent that may change the tree.
	ShapeWriter Shape = "writer"
	// ShapeReader is an agent that may write only its artifact directory.
	ShapeReader Shape = "reader"
)

// Source is one custom agent: frontmatter plus prompt body.
type Source struct {
	// Name is the agent's name; ^[a-z0-9][a-z0-9._-]{0,63}$ and never a
	// shipped agent name.
	Name string
	// Description is one line, non-empty, <= 300 runes, no newline.
	Description string
	// Shape is writer or reader.
	Shape Shape
	// Output labels what a round leaves; ^[a-z][a-z0-9-]{0,23}$.
	Output string
	// Requires names helper agents this one spawns; may be empty.
	Requires []string
	// Kinds are the harness kinds to render; empty means every known kind.
	Kinds []string
	// Body is the prompt. After Parse it ends with exactly one "\n".
	Body string
}

var (
	nameRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	outputRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,23}$`)
	// requiredKeys are the frontmatter keys a source must carry.
	requiredKeys = []string{"name", "description", "shape", "output"}
)

// syntaxErr renders a frontmatter syntax error: the line is the one that
// carried the bad text.
func syntaxErr(line int, what string) error {
	return fmt.Errorf("agent source: line %d: %s: %w", line, what, ErrBadSource)
}

// fieldErr renders a validation error against one field.
func fieldErr(name, field, what string) error {
	return fmt.Errorf("agent source %s: %s: %s: %w", name, field, what, ErrBadSource)
}

// Parse reads the source text format. On success every field is validated and
// the body ends with exactly one "\n"; every error wraps ErrBadSource.
func Parse(data []byte) (Source, error) {
	text := string(data)
	if !strings.HasPrefix(text, "---\n") {
		return Source{}, syntaxErr(1, "missing opening fence ---")
	}
	rest := text[len("---\n"):]
	lines := strings.Split(rest, "\n")

	close := -1
	for i, ln := range lines {
		if ln == "---" {
			close = i
			break
		}
	}
	if close < 0 {
		return Source{}, syntaxErr(len(lines)+1, "missing closing fence ---")
	}

	var s Source
	s.Requires = []string{}
	s.Kinds = []string{}
	seen := make(map[string]bool, len(requiredKeys)+2)
	for i := 0; i < close; i++ {
		lineNo := i + 2 // line 1 is the opening fence
		raw := lines[i]
		if strings.TrimSpace(raw) == "" {
			return Source{}, syntaxErr(lineNo, "blank line in frontmatter")
		}
		idx := strings.Index(raw, ":")
		if idx <= 0 {
			return Source{}, syntaxErr(lineNo, "not a key: value line")
		}
		key := strings.TrimSpace(raw[:idx])
		if key == "" {
			return Source{}, syntaxErr(lineNo, "not a key: value line")
		}
		value := strings.TrimSpace(raw[idx+1:])
		if !isKnownKey(key) {
			return Source{}, syntaxErr(lineNo, fmt.Sprintf("unknown key %q", key))
		}
		if seen[key] {
			return Source{}, syntaxErr(lineNo, fmt.Sprintf("duplicate key %q", key))
		}
		seen[key] = true
		switch key {
		case "name":
			s.Name = value
		case "description":
			s.Description = value
		case "shape":
			s.Shape = Shape(value)
		case "output":
			s.Output = value
		case "requires":
			items, err := parseList(value)
			if err != nil {
				return Source{}, syntaxErr(lineNo, err.Error())
			}
			s.Requires = items
		case "kinds":
			items, err := parseList(value)
			if err != nil {
				return Source{}, syntaxErr(lineNo, err.Error())
			}
			s.Kinds = items
		}
	}
	for _, key := range requiredKeys {
		if !seen[key] {
			return Source{}, syntaxErr(close+2, fmt.Sprintf("missing required key %q", key))
		}
	}

	body := strings.Join(lines[close+1:], "\n")
	body = strings.TrimPrefix(body, "\n")
	body = strings.TrimRight(body, "\n") + "\n"
	s.Body = body

	if err := s.Validate(); err != nil {
		return Source{}, err
	}
	return s, nil
}

// isKnownKey reports whether key is one of the seven frontmatter keys.
func isKnownKey(key string) bool {
	switch key {
	case "name", "description", "shape", "output", "requires", "kinds":
		return true
	}
	return false
}

// parseList reads a flow list: "[]" or "[x, y]", items trimmed and non-empty.
func parseList(v string) ([]string, error) {
	if !strings.HasPrefix(v, "[") || !strings.HasSuffix(v, "]") {
		return nil, errors.New("list must be [] or [x, y]")
	}
	inner := v[1 : len(v)-1]
	if strings.TrimSpace(inner) == "" {
		return []string{}, nil
	}
	parts := strings.Split(inner, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		item := strings.TrimSpace(p)
		if item == "" {
			return nil, errors.New("list item is empty")
		}
		out = append(out, item)
	}
	return out, nil
}

// Validate checks every field rule of the source format (§3, §4.2).
func (s Source) Validate() error {
	if !nameRe.MatchString(s.Name) {
		return fieldErr(s.Name, "name", "must match "+nameRe.String())
	}
	if s.Description == "" {
		return fieldErr(s.Name, "description", "must not be empty")
	}
	if strings.ContainsAny(s.Description, "\n\r") {
		return fieldErr(s.Name, "description", "must be one line")
	}
	if utf8.RuneCountInString(s.Description) > 300 {
		return fieldErr(s.Name, "description", "must be at most 300 runes")
	}
	if s.Shape != ShapeWriter && s.Shape != ShapeReader {
		return fieldErr(s.Name, "shape", "must be writer or reader")
	}
	if !outputRe.MatchString(s.Output) {
		return fieldErr(s.Name, "output", "must match "+outputRe.String())
	}
	seenReq := make(map[string]bool, len(s.Requires))
	for _, r := range s.Requires {
		if !nameRe.MatchString(r) {
			return fieldErr(s.Name, "requires", fmt.Sprintf("%q is not a valid agent name", r))
		}
		if r == s.Name {
			return fieldErr(s.Name, "requires", "must not require itself")
		}
		if seenReq[r] {
			return fieldErr(s.Name, "requires", fmt.Sprintf("duplicate %q", r))
		}
		seenReq[r] = true
	}
	seenKind := make(map[string]bool, len(s.Kinds))
	for _, k := range s.Kinds {
		if _, ok := harness.Lookup(k); !ok {
			return fieldErr(s.Name, "kinds", fmt.Sprintf("unknown kind %q", k))
		}
		if seenKind[k] {
			return fieldErr(s.Name, "kinds", fmt.Sprintf("duplicate %q", k))
		}
		seenKind[k] = true
	}
	for _, k := range renderedKinds(s) {
		if harness.IsShipped(k, s.Name) {
			return fieldErr(s.Name, "name", fmt.Sprintf("name %q is a shipped agent; duplicate it under another name", s.Name))
		}
	}
	if slices.Contains(renderedKinds(s), "codex") && strings.Contains(s.Body, "'''") {
		return fieldErr(s.Name, "body", "must not contain ''' when codex is rendered")
	}
	if strings.TrimSpace(s.Body) == "" {
		return fieldErr(s.Name, "body", "must not be blank")
	}
	return nil
}

// renderedKinds is the kind list a source renders to: its own kinds, or every
// known kind when Kinds is empty.
func renderedKinds(s Source) []string {
	if len(s.Kinds) > 0 {
		return s.Kinds
	}
	all := harness.All()
	out := make([]string, 0, len(all))
	for _, h := range all {
		out = append(out, h.Kind)
	}
	return out
}

// Format writes the source text format. Parse(Format(s)) equals s for any
// valid s whose Body already ends in exactly one "\n".
func Format(s Source) []byte {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("name: " + s.Name + "\n")
	b.WriteString("description: " + s.Description + "\n")
	b.WriteString("shape: " + string(s.Shape) + "\n")
	b.WriteString("output: " + s.Output + "\n")
	b.WriteString("requires: " + formatList(s.Requires) + "\n")
	b.WriteString("kinds: " + formatList(s.Kinds) + "\n")
	b.WriteString("---\n\n")
	b.WriteString(s.Body)
	return []byte(b.String())
}

// formatList writes a flow list: "[]" or "[a, b]".
func formatList(items []string) string {
	if len(items) == 0 {
		return "[]"
	}
	return "[" + strings.Join(items, ", ") + "]"
}
