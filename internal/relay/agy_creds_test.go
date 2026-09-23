package relay

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// agyCredsNow is the pinned capture time these tests use, so captured_at is
// exact and a round-trip can be compared with ==.
var agyCredsNow = time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)

// agyEnv turns a map into the func(string) string CaptureAgyCreds reads.
func agyEnv(m map[string]string) func(string) string {
	return func(key string) string { return m[key] }
}

// fullAgyEnv is the complete environment agy injects into the commands it runs.
func fullAgyEnv() map[string]string {
	return map[string]string{
		agyConversationEnv: "0f0e0d0c-0b0a-4998-8877-665544332211",
		agyLSAddressEnv:    "localhost:42139",
		agyCSRFTokenEnv:    "the-csrf-token",
		agyAgentAPIExeEnv:  "/usr/local/bin/agy",
	}
}

// TestValidConversationID pins relay's own copy of the agy conversation id
// rule. planner.Detect carries the same pattern for the environment it reads.
func TestValidConversationID(t *testing.T) {
	tests := []struct {
		id   string
		want bool
	}{
		{"0f0e0d0c-0b0a-4998-8877-665544332211", true},
		{"00000000-0000-0000-0000-000000000000", true},
		{"0f0e0d0c-0b0a-4998-8877-665544332211a", false},
		{"0f0e0d0c-0b0a-4998-8877-66554433221", false},
		{"0F0E0D0C-0B0A-4998-8877-665544332211", false},
		{"0f0e0d0c0b0a49988877665544332211", false},
		{"not-a-uuid", false},
		{"", false},
	}
	for _, tc := range tests {
		if got := validConversationID(tc.id); got != tc.want {
			t.Errorf("validConversationID(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
}

// TestAgyCaptureWritesRoundTrip is the happy path: a full environment writes
// <conv>.json mode 0600 in a 0700 directory, and ReadAgyCreds gives back
// exactly what was captured.
func TestAgyCaptureWritesRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "planners", ".agy")
	env := fullAgyEnv()

	wrote, err := CaptureAgyCreds(agyEnv(env), dir, agyCredsNow)
	if err != nil {
		t.Fatalf("CaptureAgyCreds: %v", err)
	}
	if !wrote {
		t.Fatal("a full environment must write")
	}

	path := filepath.Join(dir, env[agyConversationEnv]+".json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("file mode = %o, want 600", got)
	}

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat %s: %v", dir, err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Errorf("dir mode = %o, want 700", got)
	}

	got, err := ReadAgyCreds(dir, env[agyConversationEnv])
	if err != nil {
		t.Fatalf("ReadAgyCreds: %v", err)
	}
	want := AgyCreds{
		ConversationID: env[agyConversationEnv],
		LSAddress:      env[agyLSAddressEnv],
		CSRFToken:      env[agyCSRFTokenEnv],
		AgentAPIExe:    env[agyAgentAPIExeEnv],
		CapturedAt:     agyCredsNow,
	}
	if got != want {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

// TestAgyCaptureWritesNothingWithoutAllThree pins the gate: a missing or
// invalid conversation id, address or token writes nothing at all.
func TestAgyCaptureWritesNothingWithoutAllThree(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]string)
	}{
		{name: "no conversation id", mutate: func(m map[string]string) { delete(m, agyConversationEnv) }},
		{name: "a malformed conversation id", mutate: func(m map[string]string) { m[agyConversationEnv] = "not-a-uuid" }},
		{name: "no address", mutate: func(m map[string]string) { delete(m, agyLSAddressEnv) }},
		{name: "a non-loopback address", mutate: func(m map[string]string) { m[agyLSAddressEnv] = "10.0.0.5:42139" }},
		{name: "an address with no port", mutate: func(m map[string]string) { m[agyLSAddressEnv] = "localhost" }},
		{name: "an address with a non-numeric port", mutate: func(m map[string]string) { m[agyLSAddressEnv] = "localhost:http" }},
		{name: "a loopback-looking remote host", mutate: func(m map[string]string) { m[agyLSAddressEnv] = "127.0.0.1.evil.com:42139" }},
		{name: "no token", mutate: func(m map[string]string) { delete(m, agyCSRFTokenEnv) }},
		{name: "an empty token", mutate: func(m map[string]string) { m[agyCSRFTokenEnv] = "" }},
		{name: "a token with whitespace", mutate: func(m map[string]string) { m[agyCSRFTokenEnv] = "two words" }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := fullAgyEnv()
			tc.mutate(env)
			dir := filepath.Join(t.TempDir(), "planners", ".agy")

			wrote, err := CaptureAgyCreds(agyEnv(env), dir, agyCredsNow)
			if err != nil {
				t.Fatalf("CaptureAgyCreds: %v", err)
			}
			if wrote {
				t.Error("wrote, want nothing written")
			}
			entries, err := os.ReadDir(dir)
			if err == nil && len(entries) != 0 {
				t.Errorf("left %d entries in %s; want nothing", len(entries), dir)
			}
		})
	}
}

// TestAgyCaptureSkipsIdenticalAndRewritesChangedToken pins the two halves of
// the write rule: identical values are not rewritten (the file's mtime is
// untouched), and a changed token is.
func TestAgyCaptureSkipsIdenticalAndRewritesChangedToken(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "planners", ".agy")
	env := fullAgyEnv()
	conv := env[agyConversationEnv]
	path := filepath.Join(dir, conv+".json")

	if wrote, err := CaptureAgyCreds(agyEnv(env), dir, agyCredsNow); err != nil || !wrote {
		t.Fatalf("first capture: wrote=%v err=%v", wrote, err)
	}

	// A pinned old mtime makes "was not rewritten" provable without a sleep.
	past := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	wrote, err := CaptureAgyCreds(agyEnv(env), dir, agyCredsNow)
	if err != nil {
		t.Fatalf("second capture: %v", err)
	}
	if wrote {
		t.Error("identical values must not write")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !info.ModTime().Equal(past) {
		t.Errorf("mtime = %v, want the pinned %v; the file was rewritten", info.ModTime(), past)
	}

	env[agyCSRFTokenEnv] = "a-rotated-token"
	wrote, err = CaptureAgyCreds(agyEnv(env), dir, agyCredsNow)
	if err != nil {
		t.Fatalf("third capture: %v", err)
	}
	if !wrote {
		t.Error("a changed token must write")
	}
	got, err := ReadAgyCreds(dir, conv)
	if err != nil {
		t.Fatalf("ReadAgyCreds: %v", err)
	}
	if got.CSRFToken != "a-rotated-token" {
		t.Errorf("csrf_token = %q, want the rotated token", got.CSRFToken)
	}
}

// TestAgyCapturePrunesOldSiblings pins the prune: a write drops credential
// files whose capture is older than a week, and keeps last night's.
func TestAgyCapturePrunesOldSiblings(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "planners", ".agy")
	env := fullAgyEnv()

	oldConv := "11111111-2222-3333-4444-555555555555"
	youngConv := "99999999-8888-7777-6666-555555555555"
	writeSibling := func(conv string, capturedAt time.Time) string {
		t.Helper()
		raw := fmt.Sprintf(`{"conversation_id":%q,"ls_address":"localhost:1","csrf_token":"t","agentapi_exe":"","captured_at":%q}`,
			conv, capturedAt.Format(time.RFC3339Nano))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		path := filepath.Join(dir, conv+".json")
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatalf("write sibling: %v", err)
		}
		return path
	}

	oldPath := writeSibling(oldConv, agyCredsNow.Add(-10*24*time.Hour))
	youngPath := writeSibling(youngConv, agyCredsNow.Add(-24*time.Hour))

	if wrote, err := CaptureAgyCreds(agyEnv(env), dir, agyCredsNow); err != nil || !wrote {
		t.Fatalf("capture: wrote=%v err=%v", wrote, err)
	}

	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Errorf("10-day-old sibling still present (err=%v), want it pruned", err)
	}
	if _, err := os.Stat(youngPath); err != nil {
		t.Errorf("1-day-old sibling is gone (%v), want it kept", err)
	}
	if _, err := os.Stat(filepath.Join(dir, env[agyConversationEnv]+".json")); err != nil {
		t.Errorf("the capture itself is gone: %v", err)
	}
}

// TestAgyCredsNeverPrintsTheToken pins the redaction: every fmt spelling of an
// AgyCreds, including %#v, renders the token as <redacted>.
func TestAgyCredsNeverPrintsTheToken(t *testing.T) {
	creds := AgyCreds{
		ConversationID: "0f0e0d0c-0b0a-4998-8877-665544332211",
		LSAddress:      "localhost:42139",
		CSRFToken:      "the-csrf-token",
		AgentAPIExe:    "/usr/local/bin/agy",
		CapturedAt:     agyCredsNow,
	}

	rendered := fmt.Sprintf("%v %+v %#v", creds, creds, creds)
	if strings.Contains(rendered, creds.CSRFToken) {
		t.Errorf("a rendered AgyCreds contains the token: %s", rendered)
	}
	if !strings.Contains(rendered, redactedToken) {
		t.Errorf("a rendered AgyCreds does not say %s: %s", redactedToken, rendered)
	}
}

// TestReadAgyCredsMissingFileIsNotExist pins the error a caller keys on: a
// conversation with no captured file reports os.ErrNotExist.
func TestReadAgyCredsMissingFileIsNotExist(t *testing.T) {
	_, err := ReadAgyCreds(t.TempDir(), "0f0e0d0c-0b0a-4998-8877-665544332211")
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("ReadAgyCreds on a missing file = %v, want os.ErrNotExist", err)
	}
}
