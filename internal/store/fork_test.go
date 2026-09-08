package store

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestForkStateCopiesRoundFilesAndLog(t *testing.T) {
	s := New(t.TempDir())
	srcName := "webshop"
	dstName := "webshop-fork"

	b := newBinding(srcName, "/repo")
	b.Round = 5
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Create round files for rounds 1..5 in src.
	srcDir := s.Dir(srcName)
	for r := 1; r <= 5; r++ {
		files := map[string]string{
			s.PlanPath(srcName, r):     fmt.Sprintf("plan content for round %d", r),
			s.ReportPath(srcName, r):   fmt.Sprintf("report content for round %d", r),
			s.QuestionPath(srcName, r): fmt.Sprintf("question content for round %d", r),
			s.DiffPath(srcName, r):     fmt.Sprintf("diff content for round %d", r),
			filepath.Join(srcDir, fmt.Sprintf("%03d-custom.artifact", r)): fmt.Sprintf("custom artifact %d", r),
		}
		for path, content := range files {
			if err := os.WriteFile(path, []byte(content), bindingFileMode); err != nil {
				t.Fatalf("WriteFile %s: %v", path, err)
			}
		}

		// Write log entries for each round.
		deliveryTime := time.Now().UTC().Add(-time.Hour)
		entries := []LogEntry{
			{
				Round:     r,
				Direction: DirToBuilder,
				Kind:      KindPlan,
				Path:      s.PlanPath(srcName, r),
				Confirmed: true,
			},
			{
				Round:       r,
				Direction:   DirToPlanner,
				Kind:        KindReport,
				Path:        s.ReportPath(srcName, r),
				Confirmed:   false,
				DeliveredAt: &deliveryTime,
			},
			{
				Round:     r,
				Direction: DirToPlanner,
				Kind:      KindQuestion,
				Confirmed: false,
			},
		}
		for _, e := range entries {
			if err := s.AppendLog(srcName, e); err != nil {
				t.Fatalf("AppendLog: %v", err)
			}
		}
	}

	// Snapshot source directory before fork.
	srcEntriesBefore, err := os.ReadDir(srcDir)
	if err != nil {
		t.Fatalf("ReadDir src: %v", err)
	}
	srcSnap := make(map[string][]byte, len(srcEntriesBefore))
	for _, e := range srcEntriesBefore {
		content, err := os.ReadFile(filepath.Join(srcDir, e.Name()))
		if err != nil {
			t.Fatalf("ReadFile %s: %v", e.Name(), err)
		}
		srcSnap[e.Name()] = content
	}
	srcLogBefore, err := s.ReadLog(srcName)
	if err != nil {
		t.Fatalf("ReadLog src: %v", err)
	}

	// Fork through round 2 under the lock.
	if err := s.WithLock(func(tx *Tx) error {
		return tx.ForkState(srcName, dstName, 2)
	}); err != nil {
		t.Fatalf("ForkState: %v", err)
	}

	dstDir := s.Dir(dstName)

	// Postcondition: bind.json must NOT exist in dst.
	if _, err := os.Stat(filepath.Join(dstDir, "bind.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dst must not contain bind.json, got err: %v", err)
	}

	// Postcondition: log.jsonl must exist and contain only round 1 & 2 entries,
	// all Confirmed: true and DeliveredAt == nil.
	dstLog, err := s.ReadLog(dstName)
	if err != nil {
		t.Fatalf("ReadLog dst: %v", err)
	}
	if len(dstLog) != 6 { // 3 entries * 2 rounds
		t.Fatalf("got %d log entries in dst, want 6", len(dstLog))
	}
	for i, e := range dstLog {
		if e.Round > 2 {
			t.Errorf("entry %d has Round %d > 2", i, e.Round)
		}
		if !e.Confirmed {
			t.Errorf("entry %d Confirmed = false, want true", i)
		}
		if e.DeliveredAt != nil {
			t.Errorf("entry %d DeliveredAt = %v, want nil", i, e.DeliveredAt)
		}
	}

	// Verify dst files.
	dstEntries, err := os.ReadDir(dstDir)
	if err != nil {
		t.Fatalf("ReadDir dst: %v", err)
	}

	var dstFileNames []string
	for _, e := range dstEntries {
		dstFileNames = append(dstFileNames, e.Name())

		// Verify mode is 0644.
		info, err := e.Info()
		if err != nil {
			t.Fatalf("Info %s: %v", e.Name(), err)
		}
		if info.Mode().Perm() != 0o644 {
			t.Errorf("dst file %s has mode %o, want 0644", e.Name(), info.Mode().Perm())
		}
	}

	wantFiles := []string{
		"001-custom.artifact",
		"001-diff.patch",
		"001-plan.md",
		"001-question.md",
		"001-report.md",
		"002-custom.artifact",
		"002-diff.patch",
		"002-plan.md",
		"002-question.md",
		"002-report.md",
		"log.jsonl",
	}
	slices.Sort(dstFileNames)
	slices.Sort(wantFiles)
	if !slices.Equal(dstFileNames, wantFiles) {
		t.Fatalf("dst files = %v, want %v", dstFileNames, wantFiles)
	}

	// Verify file contents match src for copied files.
	for _, name := range wantFiles {
		if name == "log.jsonl" {
			continue
		}
		dstContent, err := os.ReadFile(filepath.Join(dstDir, name))
		if err != nil {
			t.Fatalf("ReadFile dst %s: %v", name, err)
		}
		srcContent := srcSnap[name]
		if !bytes.Equal(dstContent, srcContent) {
			t.Errorf("file %s content mismatch: dst=%q, src=%q", name, dstContent, srcContent)
		}
	}

	// Postcondition: source directory must be unchanged in any way.
	srcEntriesAfter, err := os.ReadDir(srcDir)
	if err != nil {
		t.Fatalf("ReadDir src after: %v", err)
	}
	if len(srcEntriesAfter) != len(srcEntriesBefore) {
		t.Fatalf("src file count changed: got %d, want %d", len(srcEntriesAfter), len(srcEntriesBefore))
	}
	for _, e := range srcEntriesAfter {
		content, err := os.ReadFile(filepath.Join(srcDir, e.Name()))
		if err != nil {
			t.Fatalf("ReadFile src after %s: %v", e.Name(), err)
		}
		orig, ok := srcSnap[e.Name()]
		if !ok {
			t.Errorf("new file %s in src after fork", e.Name())
		} else if !bytes.Equal(content, orig) {
			t.Errorf("file %s in src modified after fork", e.Name())
		}
	}

	srcLogAfter, err := s.ReadLog(srcName)
	if err != nil {
		t.Fatalf("ReadLog src after: %v", err)
	}
	if len(srcLogAfter) != len(srcLogBefore) {
		t.Fatalf("src log count changed: got %d, want %d", len(srcLogAfter), len(srcLogBefore))
	}
	for i := range srcLogAfter {
		if srcLogAfter[i].Confirmed != srcLogBefore[i].Confirmed {
			t.Errorf("src log entry %d Confirmed changed", i)
		}
		if (srcLogAfter[i].DeliveredAt == nil) != (srcLogBefore[i].DeliveredAt == nil) {
			t.Errorf("src log entry %d DeliveredAt changed", i)
		}
	}
}

func TestForkStateValidationAndErrors(t *testing.T) {
	s := New(t.TempDir())
	srcName := "webshop"

	if err := s.Save(newBinding(srcName, "/repo")); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// 1. ForkState into an existing directory errors.
	existingDir := s.Dir("existing")
	if err := os.Mkdir(existingDir, bindingDirMode); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	err := s.WithLock(func(tx *Tx) error {
		return tx.ForkState(srcName, "existing", 1)
	})
	if err == nil {
		t.Fatal("ForkState into existing directory must error, got nil")
	}

	// 2. ForkState from nonexistent source returns ErrNotFound.
	err = s.WithLock(func(tx *Tx) error {
		return tx.ForkState("nosuch", "dst", 1)
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("ForkState from nonexistent src: got %v, want ErrNotFound", err)
	}

	// 3. ForkState with invalid dst name errors.
	for _, bad := range []string{"", "123bad", "UPPER", "bad name"} {
		err = s.WithLock(func(tx *Tx) error {
			return tx.ForkState(srcName, bad, 1)
		})
		if err == nil {
			t.Errorf("ForkState with invalid dst %q must error, got nil", bad)
		}
	}

	// 4. ForkState with throughRound < 1 errors.
	for _, badRound := range []int{0, -1, -5} {
		err = s.WithLock(func(tx *Tx) error {
			return tx.ForkState(srcName, "validfork", badRound)
		})
		if err == nil {
			t.Errorf("ForkState with round %d must error, got nil", badRound)
		}
	}
}

func TestForkStateMidCopyFailureLeavesNoDst(t *testing.T) {
	t.Run("unreadable file failure cleans up dst", func(t *testing.T) {
		s := New(t.TempDir())
		srcName := "webshop"
		if err := s.Save(newBinding(srcName, "/repo")); err != nil {
			t.Fatalf("Save: %v", err)
		}

		// Create round files in src.
		if err := os.WriteFile(s.PlanPath(srcName, 1), []byte("plan 1"), bindingFileMode); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		unreadable := s.ReportPath(srcName, 1)
		if err := os.WriteFile(unreadable, []byte("report 1"), bindingFileMode); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		if err := os.Chmod(unreadable, 0o000); err != nil {
			t.Fatalf("Chmod 0000: %v", err)
		}
		t.Cleanup(func() {
			_ = os.Chmod(unreadable, bindingFileMode)
		})

		dstName := "dstfailed"
		err := s.WithLock(func(tx *Tx) error {
			return tx.ForkState(srcName, dstName, 1)
		})
		if err == nil {
			t.Fatal("ForkState must error when a file is unreadable, got nil")
		}

		// Ensure dst directory was removed entirely.
		if _, err := os.Stat(s.Dir(dstName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("dst directory %s must be removed on failure, got err: %v", s.Dir(dstName), err)
		}
	})

	t.Run("corrupted log failure cleans up dst", func(t *testing.T) {
		s := New(t.TempDir())
		srcName := "webshop"
		if err := s.Save(newBinding(srcName, "/repo")); err != nil {
			t.Fatalf("Save: %v", err)
		}

		// Write invalid JSON into src's log.jsonl.
		if err := os.WriteFile(s.logPath(srcName), []byte("invalid-json\n"), bindingFileMode); err != nil {
			t.Fatalf("WriteFile log: %v", err)
		}

		dstName := "dstfailedlog"
		err := s.WithLock(func(tx *Tx) error {
			return tx.ForkState(srcName, dstName, 1)
		})
		if err == nil {
			t.Fatal("ForkState must error when log is corrupt, got nil")
		}

		// Ensure dst directory was removed entirely.
		if _, err := os.Stat(s.Dir(dstName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("dst directory %s must be removed on failure, got err: %v", s.Dir(dstName), err)
		}
	})
}

func TestRoundOfFile(t *testing.T) {
	tests := []struct {
		name      string
		filename  string
		wantRound int
		wantOk    bool
	}{
		// Valid round files with known suffixes
		{"plan suffix", "001-plan.md", 1, true},
		{"report suffix", "002-report.md", 2, true},
		{"question suffix", "003-question.md", 3, true},
		{"diff suffix", "004-diff.patch", 4, true},
		// Valid round files with arbitrary/future suffixes
		{"custom text suffix", "005-custom.txt", 5, true},
		{"archive suffix", "010-backup.tar.gz", 10, true},
		{"single digit round", "1-plan.md", 1, true},
		{"large round number", "999-report.md", 999, true},
		{"multiple hyphens in filename", "002-extra-long-name.md", 2, true},
		{"four digit round", "1000-future.artifact", 1000, true},

		// Non-round files
		{"bind.json", "bind.json", 0, false},
		{"log.jsonl", "log.jsonl", 0, false},
		{"hidden worktrees", ".worktrees", 0, false},
		{"hidden archive", ".archive", 0, false},
		{"lock file", ".lock", 0, false},
		{"empty string", "", 0, false},

		// Invalid round prefixes
		{"no number before hyphen", "-plan.md", 0, false},
		{"no suffix after hyphen", "001-", 0, false},
		{"no hyphen", "001", 0, false},
		{"alphabetic prefix", "abc-plan.md", 0, false},
		{"alphanumeric prefix", "01a-plan.md", 0, false},
		{"plus prefix", "+01-plan.md", 0, false},
		{"minus prefix", "-01-plan.md", 0, false},
		{"round zero", "000-plan.md", 0, false},
		{"round single zero", "0-plan.md", 0, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotRound, gotOk := roundOfFile(tc.filename)
			if gotOk != tc.wantOk || gotRound != tc.wantRound {
				t.Errorf("roundOfFile(%q) = (%d, %v), want (%d, %v)",
					tc.filename, gotRound, gotOk, tc.wantRound, tc.wantOk)
			}
		})
	}
}

func TestStoreWorktreeDirAndPath(t *testing.T) {
	s := New("/tmp/relay-state-test")
	if got, want := s.WorktreeDir(), "/tmp/relay-state-test/.worktrees"; got != want {
		t.Errorf("WorktreeDir = %q, want %q", got, want)
	}
	if got, want := s.WorktreePath("myfork"), "/tmp/relay-state-test/.worktrees/myfork"; got != want {
		t.Errorf("WorktreePath = %q, want %q", got, want)
	}
}
