package transcript

import (
	"fmt"
	"sort"
)

// renderCodex is the table for `codex exec --json`. It was pinned from the
// 2026-09-19 capture (spec §7): a spawned [agents.*] role is a thread inside
// the same process, reported on the parent's stream as collab_tool_call
// items.
func renderCodex(obj map[string]any) []string {
	switch str(obj["type"]) {
	case "thread.started", "turn.started", "turn.completed", "item.started":
		return nil
	case "turn.failed":
		return []string{errLine(str(asMap(obj["error"])["message"]))}
	case "error":
		return []string{errLine(str(obj["message"]))}
	case "item.completed":
		item := asMap(obj["item"])
		switch str(item["type"]) {
		case "agent_message":
			if text := str(item["text"]); text != "" {
				return []string{text}
			}
			return nil
		case "reasoning":
			return nil
		case "error":
			return []string{errLine(str(item["message"]))}
		case "command_execution":
			call := toolLine("bash", map[string]any{"command": item["command"]})
			out := str(item["aggregated_output"])
			code, _ := item["exit_code"].(float64)
			if code == 0 {
				return []string{call, okLine(out)}
			}
			return []string{call, errLine(fmt.Sprintf("exit %d: %s", int(code), out))}
		case "file_change":
			changes := asList(item["changes"])
			if len(changes) == 0 {
				return []string{"[file_change]"}
			}
			var lines []string
			for _, c := range changes {
				cm := asMap(c)
				if cm == nil {
					continue
				}
				lines = append(lines, toolLine("edit", map[string]any{"path": cm["path"]}))
			}
			return lines
		case "collab_tool_call":
			tool := str(item["tool"])
			switch tool {
			case "spawn_agent":
				return []string{toolLine("spawn_agent", map[string]any{"prompt": item["prompt"]})}
			case "wait":
				var lines []string
				states := asMap(item["agents_states"])
				keys := make([]string, 0, len(states))
				for k := range states {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					st := asMap(states[k])
					if str(st["status"]) == "completed" {
						lines = append(lines, okLine(str(st["message"])))
					}
				}
				if len(lines) == 0 {
					return []string{"[collab_tool_call wait]"}
				}
				return lines
			default:
				return []string{"[collab_tool_call " + tool + "]"}
			}
		default:
			return []string{unknown(item)}
		}
	default:
		return []string{unknown(obj)}
	}
}
