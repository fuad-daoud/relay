package release

import "testing"

// mustParse is the test's ParseVersion: a version that does not parse is a
// bug in the test's own fixture, not a case under test.
func mustParse(t *testing.T, s string) Version {
	t.Helper()
	v, ok := ParseVersion(s)
	if !ok {
		t.Fatalf("ParseVersion(%q): want ok, got not ok", s)
	}
	return v
}

func TestParseVersion(t *testing.T) {
	cases := []struct {
		in   string
		want Version
		ok   bool
	}{
		{"v1.2.3", Version{Major: 1, Minor: 2, Patch: 3}, true},
		{"1.2.3", Version{Major: 1, Minor: 2, Patch: 3}, true},
		{"v0.6.0-2-gddf3d4f", Version{Major: 0, Minor: 6, Patch: 0, Suffix: "-2-gddf3d4f"}, true},
		{"v0.6.0-dirty", Version{Major: 0, Minor: 6, Patch: 0, Suffix: "-dirty"}, true},
		{"v0.7.0-8-gbd8aed0", Version{Major: 0, Minor: 7, Patch: 0, Suffix: "-8-gbd8aed0"}, true},
		// The module version an unstamped local `go build` records.
		{
			"v0.7.1-0.20260922172736-bd8aed0eb363",
			Version{Major: 0, Minor: 7, Patch: 1, Suffix: "-0.20260922172736-bd8aed0eb363"},
			true,
		},
		{"(devel)", Version{}, false},
		{"", Version{}, false},
		{"devel", Version{}, false},
		{"v1.2", Version{}, false},
		{"v1.2.x", Version{}, false},
		{"1.2.3.4", Version{}, false},
		{"v-1.2.3", Version{}, false},
	}

	for _, c := range cases {
		got, ok := ParseVersion(c.in)
		if ok != c.ok {
			t.Errorf("ParseVersion(%q) ok = %v, want %v", c.in, ok, c.ok)
			continue
		}
		if got != c.want {
			t.Errorf("ParseVersion(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

// TestNewerOrdersDescribeSuffix pins §4.2's ordering: a describe-suffixed
// running version is newer than the tag it describes and older than the next
// patch. Make a suffix always older and the first case fails.
func TestNewerOrdersDescribeSuffix(t *testing.T) {
	tests := []struct {
		running string
		latest  string
		want    bool
	}{
		// The case that must fail under the "suffix is older" mutation.
		{"v0.6.0-2-gddf3d4f", "v0.6.0", false},
		// ... and the other half of the same fact.
		{"v0.6.0-2-gddf3d4f", "v0.6.1", true},
		{"v0.6.0-dirty", "v0.6.0", false},
		// This worktree's own `git describe`.
		{"v0.7.0-8-gbd8aed0", "v0.7.0", false},
		{"v0.7.0-8-gbd8aed0", "v0.7.1", true},
		// Plain number ordering.
		{"v0.6.0", "v0.6.1", true},
		{"v0.6.1", "v0.6.0", false},
		{"v0.6.0", "v0.6.0", false},
		{"v0.9.9", "v0.10.0", true},
	}

	for _, tc := range tests {
		if got := Newer(mustParse(t, tc.running), mustParse(t, tc.latest)); got != tc.want {
			t.Errorf("Newer(%s, %s) = %v, want %v", tc.running, tc.latest, got, tc.want)
		}
	}
}

// TestNewerRefusesUnparseable pins the refusal: "(devel)" on either side is
// false, so an untagged local build is never told it is behind.
func TestNewerRefusesUnparseable(t *testing.T) {
	if _, ok := ParseVersion("(devel)"); ok {
		t.Fatal(`ParseVersion("(devel)") must not parse`)
	}

	tests := []struct {
		running string
		latest  string
	}{
		{"(devel)", "v0.7.0"},
		{"v0.6.0", "(devel)"},
		{"(devel)", "(devel)"},
		{"v0.6.0", ""},
		{"", "v0.7.0"},
		{"v0.6.0", "not-a-version"},
	}

	for _, tc := range tests {
		if NewerStrings(tc.running, tc.latest) {
			t.Errorf("NewerStrings(%q, %q) = true, want false: relevo never claims staleness it cannot prove",
				tc.running, tc.latest)
		}
	}
}
