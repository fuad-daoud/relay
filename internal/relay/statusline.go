// Package relay statusline rendering per docs/specs/2026-09-13-statusline-design.md.
package relay

import (
	"fmt"
	"time"
)

// AgeText humanises a duration into coarse units per spec §3.4:
// truncating; under a minute Ns; under an hour Nm; otherwise Nh Mm with M
// the whole minutes past the hour, always present (e.g. 1h 0m). Negative renders 0s.
func AgeText(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d/time.Second))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	hours := int(d / time.Hour)
	mins := int((d % time.Hour) / time.Minute)
	return fmt.Sprintf("%dh %dm", hours, mins)
}
