package relay

import "testing"

func TestInputEmpty(t *testing.T) {
	// Screens are written the way herdr renders a claude pane: transcript
	// above, a separator, the input box on a `❯` line, another separator,
	// the shortcut hint below.
	bare := "some transcript\n────────────────\n❯\n────────────────\n  ? for shortcuts\n"
	draft := "some transcript\n────────────────\n❯ draft\n────────────────\n  ? for shortcuts\n"
	quoted := "some transcript\n> a quoted line in the transcript\n────────────────\n❯\n────────────────\n  ? for shortcuts\n"
	olderBare := "some transcript\n────────────────\n>\n────────────────\n  ? for shortcuts\n"
	noMarker := "some transcript with no box drawn at all\n"

	tests := []struct {
		name         string
		kind         string
		screen       string
		empty, known bool
	}{
		{"claude bare marker", "claude", bare, true, true},
		{"claude draft", "claude", draft, false, true},
		// Pins the bottom-up scan: the transcript above the box holds a
		// markdown quote that begins with `>`, and the box below it is bare.
		{"claude quoted transcript line above a bare marker", "claude", quoted, true, true},
		{"claude older > marker bare", "claude", olderBare, true, true},
		{"claude no marker", "claude", noMarker, false, false},
		{"agy has no marker yet", "agy", bare, false, false},
		{"opencode has no marker yet", "opencode", bare, false, false},
		{"empty kind has no marker yet", "", bare, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			empty, known := inputEmpty(tt.kind, tt.screen)
			if empty != tt.empty || known != tt.known {
				t.Errorf("inputEmpty(%q, %q) = (%v, %v), want (%v, %v)",
					tt.kind, tt.screen, empty, known, tt.empty, tt.known)
			}
		})
	}
}

func TestFingerprintIsStable(t *testing.T) {
	if fingerprint("a") != fingerprint("a") {
		t.Error("fingerprint must be stable for identical text")
	}
	if fingerprint("a") == fingerprint("b") {
		t.Error("fingerprint must differ for different text")
	}
}
