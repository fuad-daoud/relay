package transcript

import "testing"

func TestErrorText(t *testing.T) {
	tests := []struct {
		name   string
		kind   string
		line   string
		want   string
		wantOK bool
	}{
		// codex: a top-level error event and a turn.failed both carry the
		// harness's own reason; an error item is a warning, not fatal.
		{"codex error", "codex", `{"type":"error","message":"You've hit your usage limit."}`, "You've hit your usage limit.", true},
		{"codex turn.failed", "codex", `{"type":"turn.failed","error":{"message":"quota"}}`, "quota", true},
		{"codex item.completed error", "codex", `{"type":"item.completed","item":{"type":"error","message":"Exceeded skills context budget"}}`, "", false},

		// opencode: a top-level error event is a run failure; a tool_use
		// part in an error state is a tool failure the model sees.
		{"opencode error", "opencode", `{"type":"error","error":{"message":"opencode gave up"}}`, "opencode gave up", true},
		{"opencode tool_use error", "opencode", `{"type":"tool_use","part":{"tool":"bash","state":{"status":"error","error":"exit 1"}}}`, "", false},

		// claude: a result with is_error carries its result string; an error
		// event carries its message.
		{"claude result is_error", "claude", `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"boom"}`, "boom", true},
		{"claude result ok", "claude", `{"type":"result","subtype":"success","is_error":false,"result":""}`, "", false},

		// agy: a result whose status is not SUCCESS is fatal, and its error
		// may be a string or an object.
		{"agy result error object", "agy", `{"event":"result","result":{"status":"ERROR","error":{"message":"quota"}}}`, "quota", true},
		{"agy result success", "agy", `{"event":"result","result":{"status":"SUCCESS","response":"ok"}}`, "", false},

		// a non-JSON line is false for every kind, and so is an unknown kind.
		{"non-JSON claude", "claude", `relay-exit:1`, "", false},
		{"non-JSON opencode", "opencode", `relay-exit:1`, "", false},
		{"non-JSON agy", "agy", `relay-exit:1`, "", false},
		{"non-JSON codex", "codex", `relay-exit:1`, "", false},
		{"unknown kind", "nope", `{"type":"error","message":"boom"}`, "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ErrorText(tt.kind, []byte(tt.line))
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("ErrorText(%q, %s) = (%q, %v), want (%q, %v)", tt.kind, tt.line, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}
