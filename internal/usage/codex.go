package usage

import (
	"encoding/json"
	"io"
)

type codexUsage struct {
	Input      int64 `json:"input_tokens"`
	Cached     int64 `json:"cached_input_tokens"`
	CacheWrite int64 `json:"cache_write_input_tokens"`
	Output     int64 `json:"output_tokens"`
}

type codexEvent struct {
	Type  string      `json:"type"`
	Usage *codexUsage `json:"usage"`
}

// codexStream reads a headless round's `codex exec --json` stream: one
// sample per turn.completed event that carries usage. OpenAI's
// input_tokens includes the cached part (probe 2026-09-19: 34933 input,
// 26112 cached), hence the subtraction below; reasoning_output_tokens is a
// subset of output_tokens and is not added; a sub-agent's tokens are
// folded into the parent turn by codex and priced as the candidate's
// model.
func codexStream(r io.Reader, provider, model string) []Sample {
	var out []Sample
	scanLines(r, func(line []byte) {
		var ev codexEvent
		if json.Unmarshal(line, &ev) != nil || ev.Type != "turn.completed" || ev.Usage == nil {
			return
		}
		t := Tokens{
			In:         ev.Usage.Input - ev.Usage.Cached,
			CacheRead:  ev.Usage.Cached,
			CacheWrite: ev.Usage.CacheWrite,
			Out:        ev.Usage.Output,
		}
		out = append(out, Sample{Provider: provider, Model: model, Tokens: t})
	})
	return out
}
