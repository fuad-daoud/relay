package usage

import (
	"bytes"
	"encoding/json"
	"math"
	"sort"
	"time"
)

// StepStats is what one round's builder stream says about its model steps
// (#323, #324): how many requests the model served, how many tool calls the
// model made, and the median step time and time from a step's start to its
// first output. Zero means the harness's stream does not show the figure.
type StepStats struct {
	Steps            int
	ToolCalls        int
	StepP50MS        int64
	FirstOutputP50MS int64
}

// StreamSteps counts the model steps and tool calls in a round's builder
// stream and takes the median step duration and time to first output from
// it (#323, #324). It is pure -- no I/O, no clock -- and never fails: a
// shape the harness does not emit is a zero and an undecodable line is
// skipped. It reads the harness's own JSON lines only, so the
// `relevo-exit:`/`relevo-rusage:` trailers, blanks and junk are not events.
// The rules key on event names unique to each harness, so a stream that
// holds a second run after a mid-round switch is read as one.
func StreamSteps(harness string, stream []byte) StepStats {
	var st StepStats
	var stepSamples, firstSamples []int64
	switch harness {
	case "opencode":
		opencodeSteps(stream, &st, &stepSamples, &firstSamples)
	case "claude":
		claudeSteps(stream, &st, &stepSamples, &firstSamples)
	case "agy":
		agySteps(stream, &st, &stepSamples)
	case "codex":
		codexSteps(stream, &st)
	default:
		return StepStats{}
	}
	st.StepP50MS = lowerMedian(stepSamples)
	st.FirstOutputP50MS = lowerMedian(firstSamples)
	return st
}

// eachLine calls fn for every line whose first non-space byte is "{". A
// line that is not a JSON object -- a trailer, a blank, junk, or one that
// fails to decode -- is skipped silently.
func eachLine(stream []byte, fn func(line []byte)) {
	for _, line := range bytes.Split(stream, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		fn(line)
	}
}

// lowerMedian sorts samples ascending and returns the element at (n-1)/2:
// the lower median, so an even count takes the earlier of the two middles.
// No samples is 0.
func lowerMedian(samples []int64) int64 {
	if len(samples) == 0 {
		return 0
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	return samples[(len(samples)-1)/2]
}

// opencodeStepEvent covers the fields StreamSteps reads from an opencode
// `run --format json` line: the top-level type and epoch-millisecond
// timestamp, and a part's own start time (text, reasoning) or its state's
// (tool_use).
type opencodeStepEvent struct {
	Type      string `json:"type"`
	Timestamp int64  `json:"timestamp"`
	Part      *struct {
		Time *struct {
			Start int64 `json:"start"`
		} `json:"time"`
		State *struct {
			Time *struct {
				Start int64 `json:"start"`
			} `json:"time"`
		} `json:"state"`
	} `json:"part"`
}

// opencodeSteps is the opencode rule (§4): a step is a step_start, its
// duration is the step_finish that closes it, and its first output is the
// first text, reasoning or tool_use part the model produced in it. A
// step_start with no step_finish -- the last one in a real stream -- still
// counts as a step and contributes no duration.
func opencodeSteps(stream []byte, st *StepStats, stepSamples, firstSamples *[]int64) {
	var stepStart int64
	var open, firstSeen bool
	eachLine(stream, func(line []byte) {
		var ev opencodeStepEvent
		if json.Unmarshal(line, &ev) != nil {
			return
		}
		switch ev.Type {
		case "step_start":
			st.Steps++
			stepStart, open, firstSeen = ev.Timestamp, true, false
		case "text", "reasoning", "tool_use":
			if ev.Type == "tool_use" {
				st.ToolCalls++
			}
			if !open || firstSeen {
				return
			}
			if d := opencodeOutputTime(ev) - stepStart; d >= 0 {
				*firstSamples = append(*firstSamples, d)
			}
			firstSeen = true
		case "step_finish":
			if !open {
				return
			}
			if d := ev.Timestamp - stepStart; d >= 0 {
				*stepSamples = append(*stepSamples, d)
			}
		}
	})
}

// opencodeOutputTime is when a step's first output became visible: the
// part's own start, else a tool part's state start, else the line's
// timestamp.
func opencodeOutputTime(ev opencodeStepEvent) int64 {
	switch {
	case ev.Part != nil && ev.Part.Time != nil && ev.Part.Time.Start > 0:
		return ev.Part.Time.Start
	case ev.Part != nil && ev.Part.State != nil && ev.Part.State.Time != nil && ev.Part.State.Time.Start > 0:
		return ev.Part.State.Time.Start
	}
	return ev.Timestamp
}

// claudeStepEvent covers the fields StreamSteps reads from a claude
// stream-json line: the type and subtype, the RFC3339 timestamp, the
// sub-agent link, and the message id and its content blocks.
type claudeStepEvent struct {
	Type            string  `json:"type"`
	Subtype         string  `json:"subtype"`
	Timestamp       string  `json:"timestamp"`
	ParentToolUseID *string `json:"parent_tool_use_id"`
	Message         *struct {
		ID      string `json:"id"`
		Content []struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		} `json:"content"`
	} `json:"message"`
}

// claudeStep is one assistant message id's timing state: the last user
// event seen when the id first appeared, and the id's last-seen time.
type claudeStep struct {
	start       int64 // the lastUserTS at the id's first assistant event
	hasStart    bool
	lastSeen    int64
	hasLastSeen bool
}

// claudeSteps is the claude rule (§4): a step is a distinct message.id on
// the main thread, a tool call is a distinct tool_use block id, and a
// step's duration is its last assistant event minus the user event that
// preceded it. Sub-agent traffic, marked by parent_tool_use_id, is
// excluded; events without a timestamp contribute no timing, only counts.
func claudeSteps(stream []byte, st *StepStats, stepSamples, firstSamples *[]int64) {
	var lastUser int64
	var hasUser bool
	blocks := map[string]bool{}
	steps := map[string]*claudeStep{}
	var order []string
	eachLine(stream, func(line []byte) {
		var ev claudeStepEvent
		if json.Unmarshal(line, &ev) != nil {
			return
		}
		switch ev.Type {
		case "system":
			// The init event stands in for a user event until one arrives.
			if ev.Subtype == "init" && !hasUser {
				lastUser, hasUser = claudeTS(ev.Timestamp)
			}
		case "assistant", "user":
			if ev.ParentToolUseID != nil {
				return
			}
			ts, hasTS := claudeTS(ev.Timestamp)
			if ev.Type == "user" {
				if hasTS {
					lastUser, hasUser = ts, true
				}
				return
			}
			if ev.Message == nil {
				return
			}
			for _, b := range ev.Message.Content {
				if b.Type == "tool_use" && !blocks[b.ID] {
					blocks[b.ID] = true
					st.ToolCalls++
				}
			}
			s, ok := steps[ev.Message.ID]
			if !ok {
				s = &claudeStep{start: lastUser, hasStart: hasUser}
				steps[ev.Message.ID] = s
				order = append(order, ev.Message.ID)
				st.Steps++
				if hasTS && hasUser {
					if d := ts - lastUser; d >= 0 {
						*firstSamples = append(*firstSamples, d)
					}
				}
			}
			if hasTS {
				s.lastSeen, s.hasLastSeen = ts, true
			}
		}
	})
	for _, id := range order {
		s := steps[id]
		if !s.hasStart || !s.hasLastSeen {
			continue
		}
		if d := s.lastSeen - s.start; d >= 0 {
			*stepSamples = append(*stepSamples, d)
		}
	}
}

// claudeTS parses an RFC3339 timestamp to epoch milliseconds; ok is false
// when the field is absent or unparsable, which contributes no timing.
func claudeTS(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0, false
	}
	return t.UnixMilli(), true
}

// agyStepEvent covers the fields StreamSteps reads from an agy line: the
// event name and the step_update's type, state, index and duration.
type agyStepEvent struct {
	Event      string `json:"event"`
	StepUpdate *struct {
		StepType        string  `json:"step_type"`
		State           string  `json:"state"`
		StepIndex       int     `json:"step_index"`
		DurationSeconds float64 `json:"duration_seconds"`
	} `json:"step_update"`
}

// agySteps is the agy rule (§4): a step is a distinct agent_response
// step_index that reached DONE, its duration the harness's own
// duration_seconds; a tool call is a distinct tool step_index that reached
// ACTIVE. agy carries no wall-clock timestamps, so there is no
// first-output figure.
func agySteps(stream []byte, st *StepStats, stepSamples *[]int64) {
	seenSteps := map[int]bool{}
	seenTools := map[int]bool{}
	eachLine(stream, func(line []byte) {
		var ev agyStepEvent
		if json.Unmarshal(line, &ev) != nil || ev.Event != "step_update" || ev.StepUpdate == nil {
			return
		}
		switch {
		case ev.StepUpdate.StepType == "agent_response" && ev.StepUpdate.State == "DONE":
			if seenSteps[ev.StepUpdate.StepIndex] {
				return
			}
			seenSteps[ev.StepUpdate.StepIndex] = true
			st.Steps++
			if ms := int64(math.Round(ev.StepUpdate.DurationSeconds * 1000)); ms > 0 {
				*stepSamples = append(*stepSamples, ms)
			}
		case ev.StepUpdate.StepType == "tool" && ev.StepUpdate.State == "ACTIVE":
			if seenTools[ev.StepUpdate.StepIndex] {
				return
			}
			seenTools[ev.StepUpdate.StepIndex] = true
			st.ToolCalls++
		}
	})
}

// codexStepEvent covers the fields StreamSteps reads from a codex line:
// the event type and the completed item's type.
type codexStepEvent struct {
	Type string `json:"type"`
	Item *struct {
		Type string `json:"type"`
	} `json:"item"`
}

// codexSteps is the codex rule (§4): codex exposes no model steps, so
// Steps stays 0; an item.completed that is a command, a file change or a
// sub-agent or mcp tool call is one tool call. No timing.
func codexSteps(stream []byte, st *StepStats) {
	eachLine(stream, func(line []byte) {
		var ev codexStepEvent
		if json.Unmarshal(line, &ev) != nil || ev.Type != "item.completed" || ev.Item == nil {
			return
		}
		switch ev.Item.Type {
		case "command_execution", "file_change", "collab_tool_call", "mcp_tool_call":
			st.ToolCalls++
		}
	})
}
