package store

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestBindingRoundClosedTreeJSON(t *testing.T) {
	t.Run("empty omits round_closed_tree key", func(t *testing.T) {
		b := Binding{}
		data, err := json.Marshal(b)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if _, ok := decoded["round_closed_tree"]; ok {
			t.Errorf("expected round_closed_tree key to be omitted when empty, got JSON: %s", string(data))
		}
	})

	t.Run("non-empty includes round_closed_tree key", func(t *testing.T) {
		const treeID = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
		b := Binding{
			RoundClosedTree: treeID,
		}
		data, err := json.Marshal(b)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		got, ok := decoded["round_closed_tree"]
		if !ok {
			t.Fatalf("expected round_closed_tree key to be present when non-empty, got JSON: %s", string(data))
		}
		if got != treeID {
			t.Errorf("round_closed_tree = %v, want %v", got, treeID)
		}
	})
}

func TestBindingSwitchFieldsRoundTrip(t *testing.T) {
	t.Run("round trip", func(t *testing.T) {
		s := New(t.TempDir())
		want := newBinding("webshop", "/repo")
		want.RoundSwitches = 2
		want.BuilderMissingSince = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

		if err := s.Save(want); err != nil {
			t.Fatalf("Save: %v", err)
		}
		got, err := s.Load("webshop")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got.RoundSwitches != want.RoundSwitches || !got.BuilderMissingSince.Equal(want.BuilderMissingSince) {
			t.Errorf("switch fields mismatch: got %+v, want %+v", got, want)
		}
	})

	// Only RoundSwitches is checked for omission at zero: encoding/json's
	// omitempty never treats a zero-value struct (time.Time) as empty, so
	// BuilderMissingSince always serialises regardless of the tag -- the same
	// reason BuilderScreenAt/PlannerScreenAt above carry an inert omitempty,
	// and the reason NudgedAt on Consult drops the tag rather than promise a
	// disappearance that never happens.
	t.Run("zero omits round_switches", func(t *testing.T) {
		b := Binding{}
		data, err := json.Marshal(b)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		if bytes.Contains(data, []byte("round_switches")) {
			t.Errorf("empty binding serialised \"round_switches\"; field needs omitempty, got JSON: %s", string(data))
		}
	})
}

func TestBindingWithoutConsultsSerialisesWithoutTheKeys(t *testing.T) {
	// Every bind.json already on disk was written before consults existed.
	// Loading and re-saving one must not add keys to it.
	b := newBinding("webshop", "/repo")

	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, key := range []string{"consults", "consult_cap"} {
		if bytes.Contains(raw, []byte(key)) {
			t.Errorf("empty binding serialised %q; both fields need omitempty", key)
		}
	}
}

func TestConsultRoundTripsThroughJSON(t *testing.T) {
	b := newBinding("webshop", "/repo")
	b.ConsultCap = 4
	b.Consults = []Consult{{
		ID:           "7f2a3c1d",
		Role:         "reviewer",
		Endpoint:     Endpoint{AgentName: "webshop-reviewer-7f2a3c1d", PaneID: "w2:p9", Kind: "claude"},
		Round:        3,
		AskPath:      "/state/webshop/003-7f2a3c1d-ask.md",
		FindingsPath: "/state/webshop/003-7f2a3c1d-findings.md",
		State:        ConsultRunning,
		SpawnedAt:    time.Unix(1757000000, 0).UTC(),
	}}

	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got Binding
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if len(got.Consults) != 1 {
		t.Fatalf("got %d consults, want 1", len(got.Consults))
	}
	if got.Consults[0] != b.Consults[0] {
		t.Errorf("consult did not round-trip:\n got %+v\nwant %+v", got.Consults[0], b.Consults[0])
	}
	if got.ConsultCap != 4 {
		t.Errorf("ConsultCap = %d, want 4", got.ConsultCap)
	}
}

func TestSameBindingIgnoresNothingAndCatchesAConsult(t *testing.T) {
	a := newBinding("webshop", "/repo")
	b := newBinding("webshop", "/repo")

	if !SameBinding(a, b) {
		t.Fatal("two identically-built bindings compare different")
	}

	b.Consults = []Consult{{ID: "7f2a3c1d", Role: "reviewer", State: ConsultRunning}}
	if SameBinding(a, b) {
		t.Error("a binding differing only by an appended consult compares same; the daemon would skip the save")
	}
}

func TestSameBindingCatchesAConsultStateChange(t *testing.T) {
	a := newBinding("webshop", "/repo")
	a.Consults = []Consult{{ID: "7f2a3c1d", Role: "reviewer", State: ConsultRunning}}

	b := a
	b.Consults = []Consult{{ID: "7f2a3c1d", Role: "reviewer", State: ConsultDone}}

	if SameBinding(a, b) {
		t.Error("a consult moving running -> done compares same; the daemon would never persist the transition")
	}
}

func TestEndpointHeadless(t *testing.T) {
	cases := []struct {
		name string
		mode Mode
		want bool
	}{
		{"empty mode reads as pane", "", false},
		{"explicit pane", ModePane, false},
		{"headless", ModeHeadless, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := Endpoint{Kind: "agy", Mode: c.mode}
			if got := e.Headless(); got != c.want {
				t.Errorf("Headless() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestEndpointHeadlessFieldsRoundTripAndAreOmittedWhenZero(t *testing.T) {
	// A pane endpoint written by today's relay carries none of the new keys.
	pane := Endpoint{AgentName: "webshop-builder", PaneID: "w1:p2", Kind: "opencode"}
	data, err := json.Marshal(pane)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, key := range []string{"mode", "pid", "started_at", "log_path"} {
		if bytes.Contains(data, []byte(`"`+key+`"`)) {
			t.Errorf("pane endpoint JSON carries %q: %s", key, data)
		}
	}

	// A headless endpoint carries all four and reads back equal.
	want := Endpoint{
		AgentName: "webshop-builder",
		Kind:      "agy",
		Mode:      ModeHeadless,
		PID:       4242,
		StartedAt: 1789000000,
		LogPath:   "/state/webshop/003-builder.log",
	}
	data, err = json.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got Endpoint
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got != want {
		t.Errorf("round trip: got %+v, want %+v", got, want)
	}
	if !got.Headless() {
		t.Error("decoded headless endpoint reports Headless() false")
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal map: %v", err)
	}
	if decoded["mode"] != "headless" || decoded["log_path"] != want.LogPath {
		t.Errorf("JSON keys: %s", data)
	}
	if decoded["pid"] != float64(4242) || decoded["started_at"] != float64(1789000000) {
		t.Errorf("pid/started_at must be JSON numbers: %s", data)
	}
}
