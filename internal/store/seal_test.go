package store

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestSealableTable pins the pure predicate: a closed, quiet round is
// sealable, and each in-flight reader of the round holds it back. The stream
// blocker now takes streamDrained, not PID: the "not drained" row is the one
// the plan's mutation check drops blocker 2 for.
func TestSealableTable(t *testing.T) {
	base := newBinding("webshop", "/home/dev/webshop")
	base.Round = 4

	cases := []struct {
		name    string
		b       Binding
		round   int
		drained bool
		want    bool
	}{
		{"closed and quiet", base, 3, false, true},
		{"the open round", base, 4, false, false},
		{"a future round", base, 5, false, false},
		{"done is not a blocker", withBinding(base, func(b *Binding) { b.State = StateDone }), 3, false, true},
		{"stream drain of the round is not drained", withBinding(base, func(b *Binding) {
			b.Builder.StreamRound = 3
			b.Builder.PID = 4242
		}), 3, false, false},
		{"stream drain of the round is drained", withBinding(base, func(b *Binding) {
			b.Builder.StreamRound = 3
			b.Builder.PID = 4242
		}), 3, true, true},
		{"a PID==0 drain with the trailer absent", withBinding(base, func(b *Binding) {
			b.Builder.StreamRound = 3
			b.Builder.PID = 0
		}), 3, false, false},
		{"a PID==0 drain with the trailer present", withBinding(base, func(b *Binding) {
			b.Builder.StreamRound = 3
			b.Builder.PID = 0
		}), 3, true, true},
		{"a drain of another round holds nothing back", withBinding(base, func(b *Binding) {
			b.Builder.StreamRound = 2
			b.Builder.PID = 4242
		}), 3, false, true},
		{"a running consult of the round", withBinding(base, func(b *Binding) {
			b.Consults = []Consult{{ID: "aabbccdd", Round: 3, State: ConsultRunning}}
		}), 3, false, false},
		{"a spawning consult of the round", withBinding(base, func(b *Binding) {
			b.Consults = []Consult{{ID: "aabbccdd", Round: 3, State: ConsultSpawning}}
		}), 3, false, false},
		{"a done consult of the round", withBinding(base, func(b *Binding) {
			b.Consults = []Consult{{ID: "aabbccdd", Round: 3, State: ConsultDone}}
		}), 3, false, true},
		{"a silent consult of the round", withBinding(base, func(b *Binding) {
			b.Consults = []Consult{{ID: "aabbccdd", Round: 3, State: ConsultSilent}}
		}), 3, false, true},
		{"a consult of another round", withBinding(base, func(b *Binding) {
			b.Consults = []Consult{{ID: "aabbccdd", Round: 2, State: ConsultRunning}}
		}), 3, false, true},
		{"a gate run for the round", withBinding(base, func(b *Binding) {
			b.GateRun = &GateRun{PID: 7, Round: 3}
		}), 3, false, false},
		{"a gate run for another round", withBinding(base, func(b *Binding) {
			b.GateRun = &GateRun{PID: 7, Round: 2}
		}), 3, false, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Sealable(tc.b, tc.round, tc.drained); got != tc.want {
				t.Errorf("Sealable(%s, %d, %v) = %v, want %v", tc.b.Name, tc.round, tc.drained, got, tc.want)
			}
		})
	}
}

func withBinding(b Binding, mutate func(*Binding)) Binding {
	mutate(&b)
	return b
}

// TestStreamDrained pins the drain test Sealable's stream blocker takes: a
// missing stream is drained, and a present one is drained only once the
// supervisor's exit trailer is in it and the endpoint's cursor has reached
// its size.
func TestStreamDrained(t *testing.T) {
	s := New(t.TempDir())
	b := newBinding("webshop", "/home/dev/webshop")
	b.Round = 4
	b.Builder.StreamRound = 3
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// No file at all: nothing to drain.
	if !s.StreamDrained(b, 3) {
		t.Error("StreamDrained with no stream file = false, want true")
	}

	// A round other than the one being drained is vacuously drained.
	if !s.StreamDrained(b, 2) {
		t.Error("StreamDrained of another round = false, want true")
	}

	stream := s.BuilderStreamPath(b.Name, 3)
	body := []byte(`{"type":"step"}` + "\n\n" + ExitTrailer + "0\n")
	if err := os.WriteFile(stream, body, bindingFileMode); err != nil {
		t.Fatalf("write stream: %v", err)
	}

	// The trailer is present but the cursor has not reached the stream's size.
	short := b
	short.Builder.StreamOffset = int64(len(body)) - 1
	if s.StreamDrained(short, 3) {
		t.Error("StreamDrained with the cursor short of size = true, want false")
	}

	// The trailer is present and the cursor is at the stream's size.
	done := b
	done.Builder.StreamOffset = int64(len(body))
	if !s.StreamDrained(done, 3) {
		t.Error("StreamDrained with the cursor at size = false, want true")
	}

	// No trailer, even with the cursor at EOF: the builder is still flushing.
	noTrailer := []byte("still flushing\n")
	if err := os.WriteFile(stream, noTrailer, bindingFileMode); err != nil {
		t.Fatalf("rewrite stream: %v", err)
	}
	flushing := b
	flushing.Builder.StreamOffset = int64(len(noTrailer))
	if s.StreamDrained(flushing, 3) {
		t.Error("StreamDrained without the trailer = true, want false")
	}
}

// TestSealRoundMovesOneRound pins the seal itself: exactly round 3's round
// files and consult files become rows and leave the directory, other rounds
// and non-NNN files stay, and ReadFile then returns the same bytes it did
// before.
func TestSealRoundMovesOneRound(t *testing.T) {
	s := New(t.TempDir())
	b := newBinding("webshop", "/home/dev/webshop")
	b.Round = 4
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	sealed := map[string][]byte{
		"003-plan.md":              []byte("# round 3 plan\n"),
		"003-report.md":            []byte("report with a \x00 binary byte\n"),
		"003-done":                 nil,
		"003-builder.log":          []byte("builder stderr\n"),
		"003-builder.jsonl":        []byte("{}\n"),
		"003-gate.log":             []byte("gate passed\n"),
		"003-aabbccdd-ask.md":      []byte("the question\n"),
		"003-aabbccdd-findings.md": []byte("the findings\n"),
	}
	kept := map[string][]byte{
		"002-plan.md":   []byte("round 2\n"),
		"004-plan.md":   []byte("the open round\n"),
		"land-gate.log": []byte("not a round file\n"),
	}
	for name, body := range sealed {
		if err := os.WriteFile(filepath.Join(s.Dir("webshop"), name), body, bindingFileMode); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	for name, body := range kept {
		if err := os.WriteFile(filepath.Join(s.Dir("webshop"), name), body, bindingFileMode); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	rounds, err := s.RoundsOnDisk("webshop")
	if err != nil {
		t.Fatalf("RoundsOnDisk: %v", err)
	}
	if len(rounds) != 3 || rounds[0] != 2 || rounds[1] != 3 || rounds[2] != 4 {
		t.Fatalf("RoundsOnDisk = %v, want [2 3 4]", rounds)
	}

	var n int
	if err := s.WithLock(func(tx *Tx) error {
		var err error
		n, err = tx.SealRound("webshop", 3)
		return err
	}); err != nil {
		t.Fatalf("SealRound: %v", err)
	}
	if n != len(sealed) {
		t.Errorf("SealRound sealed %d files, want %d", n, len(sealed))
	}

	for name := range sealed {
		if _, err := os.Stat(filepath.Join(s.Dir("webshop"), name)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s is still on disk", name)
		}
		got, err := s.ReadFile(filepath.Join(s.Dir("webshop"), name))
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", name, err)
		}
		if !bytes.Equal(got, sealed[name]) {
			t.Errorf("ReadFile(%s) = %q, want %q", name, got, sealed[name])
		}
	}
	for name := range kept {
		if _, err := os.Stat(filepath.Join(s.Dir("webshop"), name)); err != nil {
			t.Errorf("%s should have stayed on disk: %v", name, err)
		}
	}

	// A second seal of the same round finds nothing: the files are gone.
	if err := s.WithLock(func(tx *Tx) error {
		var err error
		n, err = tx.SealRound("webshop", 3)
		return err
	}); err != nil {
		t.Fatalf("SealRound again: %v", err)
	}
	if n != 0 {
		t.Errorf("second SealRound sealed %d files, want 0", n)
	}

	// RoundFiles sees both halves: the sealed names and what is still there.
	names, err := s.RoundFiles("webshop")
	if err != nil {
		t.Fatalf("RoundFiles: %v", err)
	}
	if !containsAll(names, kept) {
		t.Errorf("RoundFiles = %v, missing one of %v", names, keys(kept))
	}
	if !containsAll(names, sealed) {
		t.Errorf("RoundFiles = %v, missing a sealed name of %v", names, keys(sealed))
	}
}

// TestReadFileMissingStaysErrNotExist pins that a path the store never sealed
// still reports os.ReadFile's own error, so callers' ErrNotExist checks keep
// working.
func TestReadFileMissingStaysErrNotExist(t *testing.T) {
	s := New(t.TempDir())
	b := newBinding("webshop", "/home/dev/webshop")
	b.Round = 2
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Never sealed: on disk not at all, in the database never.
	path := s.ReportPath("webshop", 1)
	if _, err := s.ReadFile(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ReadFile(%s) err = %v, want ErrNotExist", path, err)
	}
	if _, _, ok, err := s.StatFile(path); ok || !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("StatFile(%s) = (ok %v, err %v), want (false, ErrNotExist)", path, ok, err)
	}

	// A non-round basename, an unknown binding and a path outside the root
	// are all plain misses.
	for _, p := range []string{
		filepath.Join(s.Dir("webshop"), "bind.json.x"),
		filepath.Join(s.Dir("nobody"), "003-report.md"),
		filepath.Join(t.TempDir(), "webshop", "003-report.md"),
	} {
		if _, err := s.ReadFile(p); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("ReadFile(%s) err = %v, want ErrNotExist", p, err)
		}
	}
}

func containsAll(haystack []string, names map[string][]byte) bool {
	have := map[string]bool{}
	for _, h := range haystack {
		have[h] = true
	}
	for name := range names {
		if !have[name] {
			return false
		}
	}
	return true
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
