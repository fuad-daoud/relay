package relevo

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// lockFreeWithin reports whether the store's state lock can be taken within d,
// by running a no-op WithLock in a goroutine.
func lockFreeWithin(t *testing.T, st *store.Store, d time.Duration) bool {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = st.WithLock(func(*store.Tx) error { return nil })
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// assertFetchIsUnlocked fails the test if a running-round fetch's network
// calls run while the state lock is held.
func assertFetchIsUnlocked(t *testing.T, st *store.Store, fr *fakeRemote) {
	t.Helper()
	fr.beforeCall = func(call string) {
		if strings.HasPrefix(call, "GetBinding:") || strings.HasPrefix(call, "RoundFileFrom:") {
			if !lockFreeWithin(t, st, 2*time.Second) {
				t.Errorf("%s ran while the state lock was held", call)
			}
		}
	}
}

func countCalls(fr *fakeRemote, prefix string) int {
	n := 0
	for _, c := range fr.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func TestTickFetchesRemoteWithoutTheStateLock(t *testing.T) {
	st := store.New(t.TempDir())
	if err := st.Save(remoteBinding("zen")); err != nil {
		t.Fatal(err)
	}
	fr := &fakeRemote{
		getBindingResp:    remote.BindingView{RoundState: remote.RoundRunning},
		roundFileFromResp: io.NopCloser(strings.NewReader("builder log line 1\n")),
	}
	assertFetchIsUnlocked(t, st, fr)
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	data, err := rt.Store.ReadFile(st.BuilderLogPath("api", 1))
	if err != nil {
		t.Fatalf("read mirrored log: %v", err)
	}
	if string(data) != "builder log line 1\n" {
		t.Fatalf("log = %q, want the fetched body", string(data))
	}
	got, err := st.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if got.Builder.RemoteStatus != string(remote.RoundRunning) {
		t.Fatalf("RemoteStatus = %q, want %q", got.Builder.RemoteStatus, remote.RoundRunning)
	}
	if n := countCalls(fr, "GetBinding:"); n != 1 {
		t.Fatalf("GetBinding calls = %d, want 1", n)
	}
}

func TestTickDiscardsFetchWhenBindingIsDoneMidFetch(t *testing.T) {
	st := store.New(t.TempDir())
	if err := st.Save(remoteBinding("zen")); err != nil {
		t.Fatal(err)
	}
	fr := &fakeRemote{
		getBindingResp:    remote.BindingView{RoundState: remote.RoundRunning},
		roundFileFromResp: io.NopCloser(strings.NewReader("builder log line 1\n")),
	}
	fr.beforeCall = func(call string) {
		if call != "GetBinding:zen:api" {
			return
		}
		if err := st.WithLock(func(tx *store.Tx) error {
			cur, err := tx.Load("api")
			if err != nil {
				return err
			}
			cur.State = store.StateDone
			return tx.Save(cur)
		}); err != nil {
			t.Fatalf("mark done mid-fetch: %v", err)
		}
	}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	got, err := st.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.StateDone {
		t.Fatalf("state = %s, want done", got.State)
	}
	if _, err := st.ReadFile(st.BuilderLogPath("api", 1)); err == nil {
		t.Fatalf("a log row was written for a discarded fetch")
	}
	if n := countCalls(fr, "GetBinding:"); n != 1 {
		t.Fatalf("GetBinding calls = %d, want 1", n)
	}
}

func TestTickDiscardsFetchWhenRoundAdvancedMidFetch(t *testing.T) {
	st := store.New(t.TempDir())
	if err := st.Save(remoteBinding("zen")); err != nil {
		t.Fatal(err)
	}
	fr := &fakeRemote{
		getBindingResp:    remote.BindingView{RoundState: remote.RoundRunning},
		roundFileFromResp: io.NopCloser(strings.NewReader("builder log line 1\n")),
	}
	fr.beforeCall = func(call string) {
		if call != "GetBinding:zen:api" {
			return
		}
		if err := st.WithLock(func(tx *store.Tx) error {
			cur, err := tx.Load("api")
			if err != nil {
				return err
			}
			cur.Round = 2
			return tx.Save(cur)
		}); err != nil {
			t.Fatalf("advance round mid-fetch: %v", err)
		}
	}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	got, err := st.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if got.Round != 2 {
		t.Fatalf("round = %d, want 2", got.Round)
	}
	if got.Builder.RemoteStatus != "" {
		t.Fatalf("RemoteStatus = %q, want it unchanged from the hook's save", got.Builder.RemoteStatus)
	}
	if _, err := st.ReadFile(st.BuilderLogPath("api", 1)); err == nil {
		t.Fatalf("a round-1 log row was written for a discarded fetch")
	}
	if n := countCalls(fr, "GetBinding:"); n != 1 {
		t.Fatalf("GetBinding calls = %d, want 1", n)
	}
}

func TestSyncRemoteFetchesWithoutTheStateLock(t *testing.T) {
	st := store.New(t.TempDir())
	if err := st.Save(remoteBinding("zen")); err != nil {
		t.Fatal(err)
	}
	fr := &fakeRemote{
		getBindingResp:    remote.BindingView{RoundState: remote.RoundRunning},
		roundFileFromResp: io.NopCloser(strings.NewReader("builder log line 1\n")),
	}
	assertFetchIsUnlocked(t, st, fr)
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	if _, err := SyncRemote(context.Background(), rt); err != nil {
		t.Fatalf("SyncRemote: %v", err)
	}

	data, err := rt.Store.ReadFile(st.BuilderLogPath("api", 1))
	if err != nil {
		t.Fatalf("read mirrored log: %v", err)
	}
	if string(data) != "builder log line 1\n" {
		t.Fatalf("log = %q, want the fetched body", string(data))
	}
	got, err := st.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if got.Builder.RemoteStatus != string(remote.RoundRunning) {
		t.Fatalf("RemoteStatus = %q, want %q", got.Builder.RemoteStatus, remote.RoundRunning)
	}
	if n := countCalls(fr, "GetBinding:"); n != 1 {
		t.Fatalf("GetBinding calls = %d, want 1", n)
	}
}

func TestApplyLogMirrorSkipsWhenTheRowMoved(t *testing.T) {
	st := store.New(t.TempDir())
	if err := st.Save(remoteBinding("zen")); err != nil {
		t.Fatal(err)
	}
	logPath := st.BuilderLogPath("api", 1)
	rt := Runtime{Store: st}

	putRow := func(body string) {
		t.Helper()
		if err := st.WithLock(func(tx *store.Tx) error {
			return tx.PutRoundFile("api", 1, logPath, []byte(body))
		}); err != nil {
			t.Fatalf("put row: %v", err)
		}
	}
	apply := func(m *logMirror) {
		t.Helper()
		if err := st.WithLock(func(tx *store.Tx) error {
			applyLogMirror(rt, tx, "api", 1, m)
			return nil
		}); err != nil {
			t.Fatalf("applyLogMirror: %v", err)
		}
	}
	readRow := func() string {
		t.Helper()
		data, err := st.ReadFile(logPath)
		if err != nil {
			t.Fatalf("read row: %v", err)
		}
		return string(data)
	}

	putRow("12345678")
	apply(&logMirror{Path: logPath, Base: 5, Body: []byte("XY")})
	if got := readRow(); got != "12345678" {
		t.Fatalf("row after a moved-row append = %q, want it unchanged", got)
	}

	putRow("12345")
	apply(&logMirror{Path: logPath, Base: 5, Body: []byte("XY")})
	if got := readRow(); got != "12345XY" {
		t.Fatalf("row after an in-place append = %q, want %q", got, "12345XY")
	}

	apply(&logMirror{Path: logPath, Base: -1, Body: []byte("ZZZ")})
	if got := readRow(); got != "ZZZ" {
		t.Fatalf("row after a full replacement = %q, want %q", got, "ZZZ")
	}
}

func TestFetchMatchesOnlyTheSameRoundAndServer(t *testing.T) {
	f := remoteFetch{Name: "api", Server: "zen", Round: 1}
	cases := []struct {
		name   string
		mutate func(*store.Binding)
		want   bool
	}{
		{"same", func(*store.Binding) {}, true},
		{"round advanced", func(b *store.Binding) { b.Round = 2 }, false},
		{"server changed", func(b *store.Binding) { b.Builder.Server = "mars" }, false},
		{"name changed", func(b *store.Binding) { b.Name = "other" }, false},
		{"done", func(b *store.Binding) { b.State = store.StateDone }, false},
		{"paused", func(b *store.Binding) { b.State = store.StatePaused }, false},
		{"not remote", func(b *store.Binding) { b.Builder.Mode = store.Mode("pane") }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := remoteBinding("zen")
			tc.mutate(&b)
			if got := f.matches(b); got != tc.want {
				t.Fatalf("matches = %v, want %v", got, tc.want)
			}
		})
	}
}
