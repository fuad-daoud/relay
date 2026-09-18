package usage

import (
	"encoding/json"
	"io"
)

type agyUsage struct {
	Input     int64 `json:"input_tokens"`
	Output    int64 `json:"output_tokens"`
	Thinking  int64 `json:"thinking_tokens"`
	CacheRead int64 `json:"cache_read_tokens"`
}

func (u agyUsage) tokens() Tokens {
	return Tokens{In: u.Input, CacheRead: u.CacheRead, Out: u.Output + u.Thinking}
}

type agyEvent struct {
	Event string `json:"event"`
	Init  *struct {
		Model string `json:"model"`
	} `json:"init"`
	StepUpdate *struct {
		StepType string    `json:"step_type"`
		Usage    *agyUsage `json:"usage"`
	} `json:"step_update"`
	Result *struct {
		Usage *agyUsage `json:"usage"`
	} `json:"result"`
}

// agyStream reads a headless round's stream. The result event's usage is
// the sample; with no result event, each agent_response step_update with
// usage is a sample. agy reports no dollars. Model: the init event's, else
// the candidate's. agy in a pane keeps no usage record at all (verified
// 2026-09-18), so there is no pane reader.
func agyStream(r io.Reader, fallbackProvider, fallbackModel string) []Sample {
	model := fallbackModel
	var result *Sample
	var steps []Sample
	scanLines(r, func(line []byte) {
		var ev agyEvent
		if json.Unmarshal(line, &ev) != nil {
			return
		}
		switch ev.Event {
		case "init":
			if ev.Init != nil && ev.Init.Model != "" {
				model = ev.Init.Model
			}
		case "step_update":
			if ev.StepUpdate != nil && ev.StepUpdate.StepType == "agent_response" && ev.StepUpdate.Usage != nil {
				steps = append(steps, Sample{Provider: fallbackProvider, Tokens: ev.StepUpdate.Usage.tokens()})
			}
		case "result":
			if ev.Result != nil && ev.Result.Usage != nil {
				result = &Sample{Provider: fallbackProvider, Tokens: ev.Result.Usage.tokens()}
			}
		}
	})
	if result != nil {
		result.Model = model
		return []Sample{*result}
	}
	for i := range steps {
		steps[i].Model = model
	}
	return steps
}
