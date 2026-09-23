package usage

import (
	"strings"
	"testing"
	"time"
)

// stepStream joins JSON lines the way a round's builder stream file holds
// them: one per line, with a blank line and the round's trailer after them.
func stepStream(lines ...string) []byte {
	return []byte(strings.Join(lines, "\n") + "\n\nrelevo-exit:0\n")
}

func TestStreamStepsOpencode(t *testing.T) {
	s := stepStream(
		`{"type":"step_start","timestamp":1000}`,
		`{"type":"text","timestamp":1500,"part":{"type":"text","time":{"start":1600,"end":1700},"text":"working"}}`,
		`{"type":"tool_use","timestamp":3900,"part":{"type":"tool","state":{"status":"completed","time":{"start":3800,"end":3900}}}}`,
		`{"type":"step_finish","timestamp":4000}`,
		`{"type":"step_start","timestamp":5000}`,
		`{"type":"tool_use","timestamp":5900,"part":{"type":"tool","state":{"time":{"start":5700,"end":5800}}}}`,
		`{"type":"tool_use","timestamp":6500,"part":{"type":"tool","state":{"time":{"start":6400,"end":6500}}}}`,
		`{"type":"step_finish","timestamp":8200}`,
		`{"type":"step_start","timestamp":9000}`,
		`{"type":"text","timestamp":9300,"part":{"type":"text","text":"done"}}`,
	)
	got := StreamSteps("opencode", s)
	if got.Steps != 3 || got.ToolCalls != 3 {
		t.Errorf("Steps/ToolCalls = %d/%d, want 3/3", got.Steps, got.ToolCalls)
	}
	if got.StepP50MS != 3000 {
		t.Errorf("StepP50MS = %d, want 3000 (lower median of 3000, 3200)", got.StepP50MS)
	}
	if got.FirstOutputP50MS != 600 {
		t.Errorf("FirstOutputP50MS = %d, want 600 (lower median of 600, 700, 300)", got.FirstOutputP50MS)
	}
}

func TestStreamStepsClaude(t *testing.T) {
	ts := func(sec int) string {
		return time.Date(2026, 9, 23, 10, 0, sec, 0, time.UTC).Format(time.RFC3339Nano)
	}
	s := stepStream(
		`{"type":"system","subtype":"init","timestamp":"`+ts(0)+`","model":"claude-sonnet-5"}`,
		`{"type":"assistant","timestamp":"`+ts(2)+`","message":{"id":"m1","content":[{"type":"tool_use","id":"tu1","name":"Bash"}]}}`,
		`{"type":"assistant","timestamp":"`+ts(3)+`","message":{"id":"m1","content":[{"type":"tool_use","id":"tu1","name":"Bash"},{"type":"text","text":"ok"}]}}`,
		`{"type":"user","timestamp":"`+ts(4)+`","message":{"role":"user","content":[{"type":"tool_result","content":"ok"}]}}`,
		`{"type":"assistant","timestamp":"`+ts(6)+`","message":{"id":"m2","content":[{"type":"tool_use","id":"tu2","name":"Bash"}]}}`,
		`{"type":"assistant","timestamp":"`+ts(7)+`","parent_tool_use_id":"tu2","message":{"id":"m9","content":[{"type":"tool_use","id":"tu9","name":"Bash"}]}}`,
	)
	got := StreamSteps("claude", s)
	if got.Steps != 2 || got.ToolCalls != 2 {
		t.Errorf("Steps/ToolCalls = %d/%d, want 2/2 (tu9 excluded, tu1 deduped)", got.Steps, got.ToolCalls)
	}
	if got.FirstOutputP50MS != 2000 {
		t.Errorf("FirstOutputP50MS = %d, want 2000 (lower median of 2000, 2000)", got.FirstOutputP50MS)
	}
	if got.StepP50MS != 2000 {
		t.Errorf("StepP50MS = %d, want 2000 (lower median of 3000, 2000)", got.StepP50MS)
	}
}

func TestStreamStepsClaudeNoTimestamps(t *testing.T) {
	s := stepStream(
		`{"type":"system","subtype":"init","model":"claude-sonnet-5"}`,
		`{"type":"assistant","message":{"id":"m1","content":[{"type":"tool_use","id":"tu1","name":"Bash"}]}}`,
		`{"type":"assistant","message":{"id":"m1","content":[{"type":"tool_use","id":"tu1","name":"Bash"},{"type":"text","text":"ok"}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"ok"}]}}`,
		`{"type":"assistant","message":{"id":"m2","content":[{"type":"tool_use","id":"tu2","name":"Bash"}]}}`,
		`{"type":"assistant","parent_tool_use_id":"tu2","message":{"id":"m9","content":[{"type":"tool_use","id":"tu9","name":"Bash"}]}}`,
	)
	got := StreamSteps("claude", s)
	if got.Steps != 2 || got.ToolCalls != 2 {
		t.Errorf("Steps/ToolCalls = %d/%d, want 2/2", got.Steps, got.ToolCalls)
	}
	if got.StepP50MS != 0 || got.FirstOutputP50MS != 0 {
		t.Errorf("P50s = %d/%d, want 0/0 without timestamps", got.StepP50MS, got.FirstOutputP50MS)
	}
}

func TestStreamStepsAgy(t *testing.T) {
	s := stepStream(
		`{"event":"init","init":{"model":"gemini-3.8-flash"}}`,
		`{"event":"step_update","step_update":{"step_index":1,"state":"DONE","step_type":"agent_response","duration_seconds":1.84}}`,
		`{"event":"step_update","step_update":{"step_index":3,"state":"DONE","step_type":"agent_response","duration_seconds":2.5}}`,
		`{"event":"step_update","step_update":{"step_index":1,"state":"DONE","step_type":"agent_response","duration_seconds":1.84}}`,
		`{"event":"step_update","step_update":{"step_index":2,"state":"ACTIVE","step_type":"tool"}}`,
		`{"event":"step_update","step_update":{"step_index":2,"state":"DONE","step_type":"tool"}}`,
	)
	got := StreamSteps("agy", s)
	if got.Steps != 2 || got.ToolCalls != 1 {
		t.Errorf("Steps/ToolCalls = %d/%d, want 2/1 (a duplicate DONE counted once)", got.Steps, got.ToolCalls)
	}
	if got.StepP50MS != 1840 {
		t.Errorf("StepP50MS = %d, want 1840 (lower median of 1840, 2500)", got.StepP50MS)
	}
	if got.FirstOutputP50MS != 0 {
		t.Errorf("FirstOutputP50MS = %d, want 0: agy has no wall-clock timestamps", got.FirstOutputP50MS)
	}
}

func TestStreamStepsCodex(t *testing.T) {
	s := stepStream(
		`{"type":"item.completed","item":{"id":"i1","type":"command_execution"}}`,
		`{"type":"item.completed","item":{"id":"i2","type":"command_execution"}}`,
		`{"type":"item.completed","item":{"id":"i3","type":"file_change"}}`,
		`{"type":"item.completed","item":{"id":"i4","type":"agent_message","text":"done"}}`,
	)
	got := StreamSteps("codex", s)
	if got.Steps != 0 || got.ToolCalls != 3 {
		t.Errorf("Steps/ToolCalls = %d/%d, want 0/3 (codex exposes no steps)", got.Steps, got.ToolCalls)
	}
}

func TestStreamStepsUnknownHarnessAndJunk(t *testing.T) {
	if got := StreamSteps("nope", stepStream(`{"type":"step_start","timestamp":1000}`)); got != (StepStats{}) {
		t.Errorf("unknown harness = %+v, want the zero StepStats", got)
	}
	junk := []byte("not json\n{broken\n[1,2]\n\n   \nrelevo-exit:0\nrelevo-rusage:in=1\n")
	if got := StreamSteps("opencode", junk); got != (StepStats{}) {
		t.Errorf("junk stream = %+v, want the zero StepStats", got)
	}
}
