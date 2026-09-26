package transcript

import "bytes"

// relevo's supervisor appends two bookkeeping lines to a builder's stream once
// the builder exits: "relevo-exit:<code>" and "relevo-rusage:<...>"; a stream
// written before the rename carries the pre-rename spellings. Neither is the
// builder's words, so Render drops them. The prefixes are spelled out here
// because internal/proc importing this package would cycle; the strings must
// stay equal to proc's constants (the stream file itself keeps the lines, and
// proc.ExitCode and proc.Rusage read them there).
const (
	relevoExitTrailer   = "relevo-exit:"
	relevoRusageTrailer = "relevo-rusage:"
	legacyExitTrailer   = "relay-exit:"   // name-guard: legacy
	legacyRusageTrailer = "relay-rusage:" // name-guard: legacy
)

var trailerPrefixes = [...]string{
	relevoExitTrailer,
	relevoRusageTrailer,
	legacyExitTrailer,
	legacyRusageTrailer,
}

func isTrailerLine(trimmed []byte) bool {
	for _, p := range trailerPrefixes {
		if bytes.HasPrefix(trimmed, []byte(p)) {
			return true
		}
	}
	return false
}
