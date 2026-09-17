package transcript

// renderAgy is the table for `agy -p --output-format stream-json` (spec
// §4.1). agy streams tool steps but not assistant text; the only text is
// result.response at the end. A tool step in a state the table does not
// know falls to rule 5 rather than being guessed at.
func renderAgy(obj map[string]any) []string {
	switch str(obj["event"]) {
	case "step_update":
		su := asMap(obj["step_update"])
		if str(su["step_type"]) != "tool" {
			return nil
		}
		info := asMap(su["tool_info"])
		switch str(su["state"]) {
		case "ACTIVE":
			name := str(su["tool_name"])
			if name == "" {
				name = str(info["name"])
			}
			return []string{toolLine(name, asMap(info["parameters"]))}
		case "DONE":
			return []string{"  -> ok"}
		case "ERROR":
			return []string{errLine(str(asMap(info["error"])["message"]))}
		}
		return []string{unknown(obj)}
	case "result":
		r := asMap(obj["result"])
		var out []string
		for _, d := range asList(r["denied_actions"]) {
			m := asMap(d)
			action := str(m["action"])
			if action == "" {
				continue
			}
			line := "denied: " + action
			if dn := str(m["display_name"]); dn != "" {
				line += " (" + dn + ")"
			}
			out = append(out, line)
		}
		if st := str(r["status"]); st != "" && st != "SUCCESS" {
			out = append(out, "result: "+st)
		}
		if resp := str(r["response"]); resp != "" {
			out = append(out, resp)
		}
		return out
	case "init":
		return nil
	}
	return []string{unknown(obj)}
}
