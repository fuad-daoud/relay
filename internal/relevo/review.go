package relevo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/fuad-daoud/relevo/internal/patch"
)

// Comment is one line of a review comments file: either anchored to a
// path:line the round's diff actually has, or general (Path == "").
type Comment struct {
	Path string
	Line int
	Text string
	Raw  string
}

// ReviewOptions is what Review needs to turn a comments file into a
// follow-up plan.
type ReviewOptions struct {
	Name        string
	File        string
	Round       int // 0 = newest completed round
	Out         string
	Send        bool
	SendOptions SendOptions
}

// ReviewResult is what one successful Review produced.
type ReviewResult struct {
	Round    int
	PlanPath string
	Comments int // anchored comments
	General  int
	Sent     *SendResult
}

var commentAnchorRE = regexp.MustCompile(`^([^\s:]+):(\d+):\s*(.*)$`)

// ParseComments reads a comments file: one path:line: text (anchored) or
// plain text (general) per non-empty, non-"#" line.
func ParseComments(b []byte) []Comment {
	var out []Comment
	for _, line := range strings.Split(string(b), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if m := commentAnchorRE.FindStringSubmatch(line); m != nil {
			n, _ := strconv.Atoi(m[2])
			out = append(out, Comment{Path: m[1], Line: n, Text: m[3], Raw: line})
			continue
		}
		out = append(out, Comment{Text: line, Raw: line})
	}
	return out
}

// Review validates opts.File's comments against the round's captured diff,
// renders a follow-up plan quoting each anchored comment's hunk, writes it,
// and -- when opts.Send -- hands it to Send as the next round.
func Review(ctx context.Context, rt Runtime, opts ReviewOptions) (ReviewResult, error) {
	b, err := rt.Store.Load(opts.Name)
	if err != nil {
		return ReviewResult{}, err
	}

	round := opts.Round
	if round == 0 {
		round = b.Round - 1
	}
	if round < 1 {
		return ReviewResult{}, fmt.Errorf("binding %q has no completed round yet", opts.Name)
	}

	raw, ok, err := ReadDiff(rt, opts.Name, round)
	if err != nil {
		return ReviewResult{}, err
	}
	if !ok {
		return ReviewResult{}, fmt.Errorf("no diff recorded for round %d of %q", round, opts.Name)
	}

	p, err := patch.Parse(raw)
	if err != nil {
		return ReviewResult{}, err
	}

	commentBytes, err := os.ReadFile(opts.File)
	if err != nil {
		return ReviewResult{}, err
	}
	comments := ParseComments(commentBytes)

	var bad []Comment
	for _, c := range comments {
		if c.Path == "" {
			continue
		}
		if _, _, ok := p.HasLine(c.Path, c.Line); !ok {
			bad = append(bad, c)
		}
	}
	if len(bad) > 0 {
		var sb strings.Builder
		fmt.Fprintf(&sb, "%d anchor(s) not in round %d's diff:\n", len(bad), round)
		for i, c := range bad {
			if i > 0 {
				sb.WriteByte('\n')
			}
			fmt.Fprintf(&sb, "  %s", c.Raw)
		}
		return ReviewResult{}, fmt.Errorf("%s", sb.String())
	}

	text := renderReviewPlan(opts.Name, round, p, comments)

	out := opts.Out
	if out == "" {
		out = filepath.Join(rt.Store.Dir(opts.Name), fmt.Sprintf("%03d-review-plan.md", round))
	}
	if err := os.WriteFile(out, []byte(text), 0o644); err != nil {
		return ReviewResult{}, err
	}

	var anchored, general int
	for _, c := range comments {
		if c.Path == "" {
			general++
		} else {
			anchored++
		}
	}

	res := ReviewResult{Round: round, PlanPath: out, Comments: anchored, General: general}

	if opts.Send {
		sres, err := Send(ctx, rt, opts.Name, out, opts.SendOptions)
		if err != nil {
			return res, err
		}
		res.Sent = &sres
	}

	return res, nil
}

// hunkQuote returns h's lines from New-3 to New+3 around target (inclusive,
// by post-image line number), with any '-' line interleaved between them
// included too, each rendered as the raw diff line (kind byte + text).
func hunkQuote(h patch.Hunk, target int) []string {
	lo, hi := target-3, target+3
	minIdx, maxIdx := -1, -1
	for i, l := range h.Lines {
		if l.Kind == '-' {
			continue
		}
		if l.New >= lo && l.New <= hi {
			if minIdx == -1 {
				minIdx = i
			}
			maxIdx = i
		}
	}
	if minIdx == -1 {
		return nil
	}
	out := make([]string, 0, maxIdx-minIdx+1)
	for i := minIdx; i <= maxIdx; i++ {
		l := h.Lines[i]
		out = append(out, string(l.Kind)+l.Text)
	}
	return out
}

// renderReviewPlan renders the follow-up plan: one task per anchored
// comment, quoting its hunk, then the general comments, then the report
// instruction.
func renderReviewPlan(name string, round int, p patch.Patch, comments []Comment) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "# Review of round %d for %s\n\n", round, name)
	sb.WriteString("Fix ONLY what these comments ask; do not restyle or refactor unrelated code. Run the project's check command in the foreground before the report. If a comment is wrong or impossible, halt and report it rather than improvise.\n\n")

	sb.WriteString("## Tasks\n")
	k := 0
	for _, c := range comments {
		if c.Path == "" {
			continue
		}
		k++
		fmt.Fprintf(&sb, "### Task %d -- %s:%d\n\n", k, c.Path, c.Line)
		sb.WriteString(c.Text)
		sb.WriteString("\n\n")
		sb.WriteString("```diff\n")
		if h, _, ok := p.HasLine(c.Path, c.Line); ok {
			for _, ln := range hunkQuote(h, c.Line) {
				sb.WriteString(ln)
				sb.WriteByte('\n')
			}
		}
		sb.WriteString("```\n\n")
	}

	sb.WriteString("## General comments\n")
	for _, c := range comments {
		if c.Path != "" {
			continue
		}
		fmt.Fprintf(&sb, "- %s\n", c.Text)
	}

	sb.WriteString("## Report\nWrite the report the handoff prompt names; list each task's outcome.")

	return sb.String()
}
