package relay

import (
	"reflect"
	"strings"
	"testing"
)

// relayBlock wraps body in the fenced block a report or a findings file ends
// with, so the tests below read as the artefact rather than the fence.
func relayBlock(body string) string {
	return "findings in prose\n\n```relay\n" + body + "\n```\n"
}

// TestVerifyQuestionNamesEveryFile pins what the reviewer is handed (#144):
// the plan, the report, the diff and the gate log paths, "none" for a round
// with no gate, and the block template it must answer with. Mutation check:
// drop any one argument from the format string and this fails.
func TestVerifyQuestionNamesEveryFile(t *testing.T) {
	q := verifyQuestion("webshop", 1,
		"/state/webshop/001-plan.md",
		"/state/webshop/001-report.md",
		"/state/webshop/001-diff.patch",
		"/state/webshop/001-gate.log")

	for _, want := range []string{
		"Verify round 1 of binding \"webshop\"",
		"/state/webshop/001-plan.md",
		"/state/webshop/001-report.md",
		"/state/webshop/001-diff.patch",
		"/state/webshop/001-gate.log",
		"verdict: accepted | rejected",
		"reasons: [\"...\"]",
		"```relay",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("question does not name %q:\n%s", want, q)
		}
	}

	if q := verifyQuestion("webshop", 1, "p", "r", "d", ""); !strings.Contains(q, "Gate:   none") {
		t.Errorf("empty gate log must read \"Gate:   none\", got:\n%s", q)
	}
}

// TestParseVerdict is the table for #144's block parser: the two verdicts,
// both reason forms, and the unstructured answers that must not be read as a
// judgement.
func TestParseVerdict(t *testing.T) {
	cases := []struct {
		name        string
		findings    string
		wantVerdict string
		wantReasons []string
	}{
		{
			name:        "accepted",
			findings:    relayBlock("verdict: accepted"),
			wantVerdict: "accepted",
		},
		{
			name:        "rejected with a JSON reasons array",
			findings:    relayBlock("verdict: rejected\nreasons: [\"a\", \"b\"]"),
			wantVerdict: "rejected",
			wantReasons: []string{"a", "b"},
		},
		{
			name:        "rejected with a YAML-ish reason list",
			findings:    relayBlock("verdict: rejected\nreasons:\n- a\n- b"),
			wantVerdict: "rejected",
			wantReasons: []string{"a", "b"},
		},
		{
			name:        "no block at all",
			findings:    "just prose, no verdict block",
			wantVerdict: "unstructured",
		},
		{
			name:        "a verdict that is neither word",
			findings:    relayBlock("verdict: maybe"),
			wantVerdict: "unstructured",
		},
		{
			name: "the last block wins",
			findings: relayBlock("verdict: rejected\nreasons: [\"stale\"]") +
				"\nmore prose\n\n" + relayBlock("verdict: accepted"),
			wantVerdict: "accepted",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			verdict, reasons := parseVerdict([]byte(c.findings))
			if verdict != c.wantVerdict {
				t.Errorf("verdict = %q, want %q", verdict, c.wantVerdict)
			}
			if !reflect.DeepEqual(reasons, c.wantReasons) {
				t.Errorf("reasons = %v, want %v", reasons, c.wantReasons)
			}
		})
	}

	if v, _ := parseVerdict(nil); v != "unstructured" {
		t.Errorf("parseVerdict(nil) = %q, want unstructured", v)
	}
}
