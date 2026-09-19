package relay

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseReportTail(t *testing.T) {
	t.Run("complete block with every key", func(t *testing.T) {
		input := `Some report prose here.

` + "```relay" + `
status: halted
halted_at: "Task 2 step 3"
changed_paths: ["internal/relay/send.go", "internal/relay/deliver.go"]
commands_run: ["go test ./...", "make check"]
not_done: ["cleanup tmp", "docs"]
` + "```" + `
`
		tail, ok := ParseReportTail([]byte(input))
		if !ok {
			t.Fatalf("expected ok=true, got false")
		}
		expected := ReportTail{
			Status:       OutcomeHalted,
			HaltedAt:     "Task 2 step 3",
			ChangedPaths: []string{"internal/relay/send.go", "internal/relay/deliver.go"},
			CommandsRun:  []string{"go test ./...", "make check"},
			NotDone:      []string{"cleanup tmp", "docs"},
		}
		if !reflect.DeepEqual(tail, expected) {
			t.Fatalf("expected %+v, got %+v", expected, tail)
		}
	})

	t.Run("block with only status done", func(t *testing.T) {
		input := "```relay\nstatus: done\n```\n"
		tail, ok := ParseReportTail([]byte(input))
		if !ok {
			t.Fatalf("expected ok=true, got false")
		}
		expected := ReportTail{
			Status: OutcomeDone,
		}
		if !reflect.DeepEqual(tail, expected) {
			t.Fatalf("expected %+v, got %+v", expected, tail)
		}
	})

	t.Run("each of the four statuses", func(t *testing.T) {
		statuses := []string{OutcomeDone, OutcomeHalted, OutcomeBlocked, OutcomeDeferred}
		for _, s := range statuses {
			input := "```relay\nstatus: " + s + "\n```"
			tail, ok := ParseReportTail([]byte(input))
			if !ok || tail.Status != s {
				t.Fatalf("expected status %s and ok=true, got %s and %v", s, tail.Status, ok)
			}
		}
	})

	t.Run("unknown status -> false", func(t *testing.T) {
		badStatuses := []string{"unstructured", "DONE", "running", "failed", "pending"}
		for _, s := range badStatuses {
			input := "```relay\nstatus: " + s + "\n```"
			if _, ok := ParseReportTail([]byte(input)); ok {
				t.Fatalf("expected ok=false for status %q, got true", s)
			}
		}
	})

	t.Run("missing block -> false", func(t *testing.T) {
		inputs := [][]byte{
			nil,
			[]byte(""),
			[]byte("Just some report with no relay block\n"),
		}
		for _, inp := range inputs {
			if _, ok := ParseReportTail(inp); ok {
				t.Fatalf("expected ok=false for missing block, got true")
			}
		}
	})

	t.Run("block not at the end (prose after the closing fence) -> false", func(t *testing.T) {
		input := "```relay\nstatus: done\n```\nSome prose after the closing fence.\n"
		if _, ok := ParseReportTail([]byte(input)); ok {
			t.Fatalf("expected ok=false for prose after closing fence, got true")
		}
	})

	t.Run("an earlier ```relay block followed by a later one -> the later one wins", func(t *testing.T) {
		input := `
` + "```relay" + `
status: halted
halted_at: "step 1"
` + "```" + `

Some prose in between.

` + "```relay" + `
status: done
` + "```" + `
`
		tail, ok := ParseReportTail([]byte(input))
		if !ok {
			t.Fatalf("expected ok=true, got false")
		}
		if tail.Status != OutcomeDone || tail.HaltedAt != "" {
			t.Fatalf("expected later block to win (status: done, halted_at: empty), got %+v", tail)
		}
	})

	t.Run("unclosed fence -> false", func(t *testing.T) {
		input := "```relay\nstatus: done\n"
		if _, ok := ParseReportTail([]byte(input)); ok {
			t.Fatalf("expected ok=false for unclosed fence, got true")
		}
	})

	t.Run("unknown key ignored", func(t *testing.T) {
		input := "```relay\nstatus: done\ncustom_field: some_val\nanother_key: 123\n```\n"
		tail, ok := ParseReportTail([]byte(input))
		if !ok {
			t.Fatalf("expected ok=true, got false")
		}
		if tail.Status != OutcomeDone {
			t.Fatalf("expected status: done, got %s", tail.Status)
		}
	})

	t.Run("repeated key last wins", func(t *testing.T) {
		input := `
` + "```relay" + `
status: halted
halted_at: "first step"
status: done
halted_at: "second step"
changed_paths: ["foo.go"]
changed_paths: []
` + "```" + `
`
		tail, ok := ParseReportTail([]byte(input))
		if !ok {
			t.Fatalf("expected ok=true, got false")
		}
		if tail.Status != OutcomeDone {
			t.Fatalf("expected status: done, got %s", tail.Status)
		}
		if tail.HaltedAt != "second step" {
			t.Fatalf("expected halted_at: 'second step', got %q", tail.HaltedAt)
		}
		if tail.ChangedPaths != nil {
			t.Fatalf("expected changed_paths: nil (from []), got %+v", tail.ChangedPaths)
		}
	})

	t.Run("# comments outside and inside quotes", func(t *testing.T) {
		input := `
` + "```relay" + `
status: done # this is a comment
halted_at: "step #1" # comment after quote
changed_paths: ["path/with#hash", "path2"] # comment after list
commands_run: [cmd #not_comment]
` + "```" + `
`
		tail, ok := ParseReportTail([]byte(input))
		if !ok {
			t.Fatalf("expected ok=true, got false")
		}
		if tail.Status != OutcomeDone {
			t.Fatalf("expected status: done, got %s", tail.Status)
		}
		if tail.HaltedAt != "step #1" {
			t.Fatalf("expected halted_at 'step #1', got %q", tail.HaltedAt)
		}
		expectedPaths := []string{"path/with#hash", "path2"}
		if !reflect.DeepEqual(tail.ChangedPaths, expectedPaths) {
			t.Fatalf("expected paths %+v, got %+v", expectedPaths, tail.ChangedPaths)
		}
		expectedCmds := []string{"cmd #not_comment"}
		if !reflect.DeepEqual(tail.CommandsRun, expectedCmds) {
			t.Fatalf("expected cmds %+v, got %+v", expectedCmds, tail.CommandsRun)
		}
	})

	t.Run("[] -> nil", func(t *testing.T) {
		input := `
` + "```relay" + `
status: done
changed_paths: []
commands_run: [  ]
not_done: []
` + "```" + `
`
		tail, ok := ParseReportTail([]byte(input))
		if !ok {
			t.Fatalf("expected ok=true, got false")
		}
		if tail.ChangedPaths != nil {
			t.Fatalf("expected nil changed_paths, got %+v", tail.ChangedPaths)
		}
		if tail.CommandsRun != nil {
			t.Fatalf("expected nil commands_run, got %+v", tail.CommandsRun)
		}
		if tail.NotDone != nil {
			t.Fatalf("expected nil not_done, got %+v", tail.NotDone)
		}
	})

	t.Run("bare scalar for a list key -> one element", func(t *testing.T) {
		input := `
` + "```relay" + `
status: done
changed_paths: "single/file.go"
commands_run: make test
not_done: 'leave for later'
` + "```" + `
`
		tail, ok := ParseReportTail([]byte(input))
		if !ok {
			t.Fatalf("expected ok=true, got false")
		}
		if !reflect.DeepEqual(tail.ChangedPaths, []string{"single/file.go"}) {
			t.Fatalf("expected ['single/file.go'], got %+v", tail.ChangedPaths)
		}
		if !reflect.DeepEqual(tail.CommandsRun, []string{"make test"}) {
			t.Fatalf("expected ['make test'], got %+v", tail.CommandsRun)
		}
		if !reflect.DeepEqual(tail.NotDone, []string{"leave for later"}) {
			t.Fatalf("expected ['leave for later'], got %+v", tail.NotDone)
		}
	})

	t.Run("a line without : -> false", func(t *testing.T) {
		input := "```relay\nstatus: done\njust a random line without colon\n```\n"
		if _, ok := ParseReportTail([]byte(input)); ok {
			t.Fatalf("expected ok=false for line without colon, got true")
		}
	})

	t.Run("block list of three", func(t *testing.T) {
		input := `
` + "```relay" + `
status: done
halted_at: ""
changed_paths:
  - Makefile
  - scripts/check-plugin-version_test.sh
  - internal/relay/reporttail.go
commands_run:
  - make check
not_done:
  - docs
` + "```" + `
`
		tail, ok := ParseReportTail([]byte(input))
		if !ok {
			t.Fatalf("expected ok=true, got false")
		}
		if tail.Status != OutcomeDone {
			t.Fatalf("expected status done, got %s", tail.Status)
		}
		wantPaths := []string{"Makefile", "scripts/check-plugin-version_test.sh", "internal/relay/reporttail.go"}
		if !reflect.DeepEqual(tail.ChangedPaths, wantPaths) {
			t.Fatalf("changed_paths: got %+v, want %+v", tail.ChangedPaths, wantPaths)
		}
		if !reflect.DeepEqual(tail.CommandsRun, []string{"make check"}) {
			t.Fatalf("commands_run: got %+v", tail.CommandsRun)
		}
		if !reflect.DeepEqual(tail.NotDone, []string{"docs"}) {
			t.Fatalf("not_done: got %+v", tail.NotDone)
		}
	})

	t.Run("block list after a flow list on another key", func(t *testing.T) {
		input := `
` + "```relay" + `
status: done
commands_run: ["make check"]
changed_paths:
  - Makefile
  - scripts/check-plugin-version_test.sh
` + "```" + `
`
		tail, ok := ParseReportTail([]byte(input))
		if !ok {
			t.Fatalf("expected ok=true, got false")
		}
		if !reflect.DeepEqual(tail.CommandsRun, []string{"make check"}) {
			t.Fatalf("commands_run: got %+v", tail.CommandsRun)
		}
		wantPaths := []string{"Makefile", "scripts/check-plugin-version_test.sh"}
		if !reflect.DeepEqual(tail.ChangedPaths, wantPaths) {
			t.Fatalf("changed_paths: got %+v, want %+v", tail.ChangedPaths, wantPaths)
		}
	})

	t.Run("dash with no open list key -> false", func(t *testing.T) {
		input := "```relay\nstatus: done\n- Makefile\n```\n"
		if _, ok := ParseReportTail([]byte(input)); ok {
			t.Fatalf("expected ok=false for list item with no open list key, got true")
		}
		_, _, reason := parseReportTail([]byte(input))
		if !strings.Contains(reason, "has no ':'") {
			t.Fatalf("reason = %q, want tail: line N has no ':'", reason)
		}
	})

	t.Run("mixed flow then block on one key last wins", func(t *testing.T) {
		input := `
` + "```relay" + `
status: done
changed_paths: ["old.go"]
changed_paths:
  - Makefile
  - scripts/check-plugin-version_test.sh
` + "```" + `
`
		tail, ok := ParseReportTail([]byte(input))
		if !ok {
			t.Fatalf("expected ok=true, got false")
		}
		want := []string{"Makefile", "scripts/check-plugin-version_test.sh"}
		if !reflect.DeepEqual(tail.ChangedPaths, want) {
			t.Fatalf("changed_paths: got %+v, want %+v", tail.ChangedPaths, want)
		}
	})

	t.Run("rejected block reports why", func(t *testing.T) {
		input := "```relay\nstatus: done\njust a random line without colon\n```\n"
		_, ok, reason := parseReportTail([]byte(input))
		if ok {
			t.Fatalf("expected ok=false")
		}
		if reason != "tail: line 3 has no ':'" {
			t.Fatalf("reason = %q, want tail: line 3 has no ':'", reason)
		}
	})

	t.Run("missing block has empty reason", func(t *testing.T) {
		_, ok, reason := parseReportTail([]byte("plain prose\n"))
		if ok {
			t.Fatalf("expected ok=false")
		}
		if reason != "" {
			t.Fatalf("reason = %q, want empty", reason)
		}
	})

	t.Run("a report whose prose contains a ```go fence before the relay block still parses", func(t *testing.T) {
		input := `Here is some Go code:

` + "```go" + `
func main() {
    println("hello")
}
` + "```" + `

And here is the tail:

` + "```relay" + `
status: done
` + "```" + `
`
		tail, ok := ParseReportTail([]byte(input))
		if !ok {
			t.Fatalf("expected ok=true, got false")
		}
		if tail.Status != OutcomeDone {
			t.Fatalf("expected status: done, got %s", tail.Status)
		}
	})
}
