package transcript

// renderOpencode is the table for `opencode run --format json`. It is
// provisional (spec §1 scope boundary): the flag is verified, the event
// shapes are not -- only an error event was captured live. Every other
// event falls to rule 5, which is noise, not silence, until a live capture
// pins the table (spec §7 step 6).
func renderOpencode(obj map[string]any) []string {
	if str(obj["type"]) == "error" {
		return []string{errLine(str(asMap(obj["error"])["message"]))}
	}
	return []string{unknown(obj)}
}
