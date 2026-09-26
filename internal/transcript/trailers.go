package transcript

import "bytes"

// relevo's supervisor appends two bookkeeping lines to a builder's stream
// once the builder exits: the exit trailer, "relevo-exit:<code>", and the
// rusage trailer, "relevo-rusage:<...>". A stream written before the rename
// carries the pre-rename spellings instead. Neither is the builder's words, so
// Render drops them rather than passing them through as text. They are not
// removed from the stream file itself, which is the record proc.ExitCode and
// proc.Rusage read.
//
// The two current prefixes are proc.ExitTrailer and proc.RusageTrailer.
// This package cannot name those constants: internal/proc imports
// internal/relevo, which imports this package, so the import would be a
// cycle. The strings are spelled out here instead and must stay equal.
const (
	relevoExitTrailer   = "relevo-exit:"
	relevoRusageTrailer = "relevo-rusage:"
	legacyExitTrailer   = "relay-exit:"   // name-guard: legacy
	legacyRusageTrailer = "relay-rusage:" // name-guard: legacy
)

// trailerPrefixes is every prefix a supervisor trailer line begins with,
// after its whitespace is trimmed: the current relevo forms and the
// pre-rename ones.
var trailerPrefixes = [...]string{
	relevoExitTrailer,
	relevoRusageTrailer,
	legacyExitTrailer,
	legacyRusageTrailer,
}

// isTrailerLine reports whether a line, its whitespace already trimmed, is
// one of relevo's supervisor trailers rather than the builder's own output.
func isTrailerLine(trimmed []byte) bool {
	for _, p := range trailerPrefixes {
		if bytes.HasPrefix(trimmed, []byte(p)) {
			return true
		}
	}
	return false
}
