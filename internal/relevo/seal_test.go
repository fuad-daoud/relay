package relevo

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/ingest"
)

// TestClosedRoundSealsAfterTwoTicks is the P3c seal's end-to-end contract
// (§4.3, §4.4): the tick that closes round 1 leaves its files alone, the next
// tick moves them into the store's database and deletes them -- and every
// reader that used to open them still returns the same content: Show,
// ReadDiff, Pull's PushText, ingest's StoreSource, a fork cut through the
// round, and gc's tarball.
func TestClosedRoundSealsAfterTwoTicks(t *testing.T) {
	rt, _ := sentBinding(t)

	report := []byte("round 1's report\n")
	diff := []byte("diff --git a/x b/x\n--- a/x\n+++ b/x\n")
	logText := []byte("builder output\n")
	for path, body := range map[string][]byte{
		rt.Store.ReportPath("webshop", 1):     report,
		rt.Store.DiffPath("webshop", 1):       diff,
		rt.Store.BuilderLogPath("webshop", 1): logText,
	} {
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	// The ingest mirror is what turns the sealed file back into a round
	// artifact, so the test runs the daemon's ingest hook too.
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer d.Close()
	rt.DB = d

	// Tick 1 closes round 1. Its files must survive: the round it closes is
	// still the binding's round, so nothing is sealable yet.
	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("first Tick: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if b.Round != 2 {
		t.Fatalf("round after the close = %d, want 2", b.Round)
	}
	if _, err := os.Stat(rt.Store.ReportPath("webshop", 1)); err != nil {
		t.Fatalf("round 1's files must survive the tick that closed it: %v", err)
	}

	// Tick 2 seals it: the files are gone from the directory.
	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("second Tick: %v", err)
	}
	for _, base := range []string{"001-plan.md", "001-report.md", "001-diff.patch", "001-builder.log", "001-done"} {
		if _, err := os.Stat(filepath.Join(rt.Store.Dir("webshop"), base)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s is still on disk after the seal", base)
		}
	}

	// -- every reader now answers from the database.

	got, err := rt.Store.ReadFile(rt.Store.ReportPath("webshop", 1))
	if err != nil || !bytes.Equal(got, report) {
		t.Errorf("ReadFile(report) = %q (err %v), want %q", got, err, report)
	}

	res, err := Show(context.Background(), rt, ShowOptions{Name: "webshop", Round: 1, Section: ShowReport})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if res.Missing || res.Text != string(report) {
		t.Errorf("Show(report) = %q (missing %v), want %q", res.Text, res.Missing, report)
	}

	patch, ok, err := ReadDiff(rt, "webshop", 1)
	if err != nil || !ok || !bytes.Equal(patch, diff) {
		t.Errorf("ReadDiff = %q (ok %v, err %v), want %q", patch, ok, err, diff)
	}

	text, found, err := Pull(context.Background(), rt, "webshop", PullOptions{})
	if err != nil || !found {
		t.Fatalf("Pull: found=%v err=%v", found, err)
	}
	if !strings.Contains(text, string(report)) {
		t.Errorf("Pull text does not carry the sealed report: %q", text)
	}

	src := ingest.StoreSource(rt.Store, "webshop")
	rc, size, err := src.Open("001-report.md")
	if err != nil {
		t.Fatalf("StoreSource.Open: %v", err)
	}
	body, err := io.ReadAll(rc)
	rc.Close()
	if err != nil || !bytes.Equal(body, report) {
		t.Errorf("StoreSource.Open(report) = %q (err %v), want %q", body, err, report)
	}
	if size != int64(len(report)) {
		t.Errorf("StoreSource.Open(report) size = %d, want %d", size, len(report))
	}
	names, err := src.List()
	if err != nil || !slices.Contains(names, "001-report.md") {
		t.Errorf("StoreSource.List = %v (err %v), want the sealed 001-report.md", names, err)
	}

	// The end-of-tick ingest ran after the seal and still recorded the
	// round's report artifact from the database row.
	row, found, err := d.Binding("webshop")
	if err != nil || !found {
		t.Fatalf("Binding(webshop): found=%v err=%v", found, err)
	}
	rounds, err := d.Rounds(row.ID)
	if err != nil || len(rounds) == 0 {
		t.Fatalf("Rounds: %v (err %v)", rounds, err)
	}
	art, found, err := d.Artifact(rounds[0].ID, db.ArtifactReport)
	if err != nil || !found {
		t.Fatalf("Artifact(report): found=%v err=%v", found, err)
	}
	if art.Text != string(report) {
		t.Errorf("ingested report artifact = %q, want %q", art.Text, report)
	}

	// A fork cut through round 1 gets the sealed files as files.
	if err := rt.Store.ForkState("webshop", "forked", 1); err != nil {
		t.Fatalf("ForkState: %v", err)
	}
	forked, err := os.ReadFile(filepath.Join(rt.Store.Dir("forked"), "001-report.md"))
	if err != nil || !bytes.Equal(forked, report) {
		t.Errorf("forked report = %q (err %v), want %q", forked, err, report)
	}

	// gc's tarball holds them too.
	dest, err := rt.Store.Archive("webshop")
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if tarBody := tarMember(t, dest, "001-report.md"); !bytes.Equal(tarBody, report) {
		t.Errorf("archived report = %q, want %q", tarBody, report)
	}
}

// tarMember returns the contents of the basename member of a gzipped tarball,
// or fails the test when the member is absent.
func tarMember(t *testing.T, path, base string) []byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			t.Fatalf("archive %s has no member %s", path, base)
		}
		if err != nil {
			t.Fatalf("tar: %v", err)
		}
		if filepath.Base(h.Name) != base {
			continue
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read %s: %v", base, err)
		}
		return body
	}
}
