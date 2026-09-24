package main

import "testing"

// TestBareArgs pins round 3, step 3.1's four combinations: a bare `relevo`
// becomes `ui` only when stdin and stdout are both terminals. It never runs
// the ui, so no terminal is needed.
func TestBareArgs(t *testing.T) {
	cases := []struct {
		name                string
		stdinTTY, stdoutTTY bool
		want                []string
	}{
		{"both terminals", true, true, []string{"ui"}},
		{"stdin only", true, false, nil},
		{"stdout only", false, true, nil},
		{"neither", false, false, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := bareArgs(tc.stdinTTY, tc.stdoutTTY)
			if len(got) != len(tc.want) {
				t.Fatalf("bareArgs(%v, %v) = %v, want %v", tc.stdinTTY, tc.stdoutTTY, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("bareArgs(%v, %v) = %v, want %v", tc.stdinTTY, tc.stdoutTTY, got, tc.want)
					break
				}
			}
		})
	}
}

// TestUIStart pins round 3, step 3.2's positional mapping: no args starts at
// :fleet, a leading ':' names the view and the rest are its args, and a first
// arg without ':' is refused. Pure, so no terminal is needed.
func TestUIStart(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"empty", nil, ""},
		{"one view", []string{":rounds"}, "rounds"},
		{"view and args", []string{":rounds", "harness:agy"}, "rounds harness:agy"},
		{"round with a number", []string{":round", "webshop", "3"}, "round webshop 3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := uiStart(tc.args)
			if err != nil {
				t.Fatalf("uiStart(%v) = %v", tc.args, err)
			}
			if got != tc.want {
				t.Errorf("uiStart(%v) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}

	// A first arg without ':' fails with the exact message and no start.
	_, err := uiStart([]string{"rounds"})
	if err == nil {
		t.Fatal(`uiStart(["rounds"]) must fail`)
	}
	want := `relevo ui: want :<view> (e.g. relevo ui :rounds), got "rounds"`
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}
