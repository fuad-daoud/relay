package relevo

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The agy variables relevo reads. agy rotates ANTIGRAVITY_LS_ADDRESS and
// ANTIGRAVITY_CSRF_TOKEN on every launch, keeps neither in its own process
// environment nor on disk, and injects both only into the commands it runs
// (#349) -- so a relevo command running inside agy is the one moment they can be
// captured.
const (
	agyConversationEnv = "ANTIGRAVITY_CONVERSATION_ID"
	agyLSAddressEnv    = "ANTIGRAVITY_LS_ADDRESS"
	agyCSRFTokenEnv    = "ANTIGRAVITY_CSRF_TOKEN"
	agyAgentAPIExeEnv  = "ANTIGRAVITY_AGENTAPI_EXE"
)

// agyCredsPruneAfter is how long an unused conversation's credentials are kept
// after the last capture. A conversation that has not touched relevo for a week
// is gone, and its token died with the agy launch that issued it.
const agyCredsPruneAfter = 7 * 24 * time.Hour

// agyCredsDirMode and agyCredsFileMode are the permissions the credentials
// directory and files are created with: private to the user, because the file
// holds a bearer token for a running language server.
const (
	agyCredsDirMode  = 0o700
	agyCredsFileMode = 0o600
)

// redactedToken is what every rendering of a credential replaces the token
// with, so an accidental %v cannot leak it.
const redactedToken = "<redacted>"

// AgyCreds is one agy conversation's captured agentapi credentials, stored as
// <root>/planners/.agy/<conversation_id>.json. The conversation id is the
// file's base name and the key, not the planner record's id: Deliver receives
// only an Endpoint, whose Kind and SessionID are the agy conversation.
type AgyCreds struct {
	ConversationID string    `json:"conversation_id"`
	LSAddress      string    `json:"ls_address"`
	CSRFToken      string    `json:"csrf_token"`
	AgentAPIExe    string    `json:"agentapi_exe"`
	CapturedAt     time.Time `json:"captured_at"`
}

// String renders the credentials with the token redacted, so `%v` and `%+v` on
// an AgyCreds -- a debug print, a wrapped error message -- cannot leak it.
func (c AgyCreds) String() string {
	return fmt.Sprintf("relevo.AgyCreds{conversation_id: %q, ls_address: %q, csrf_token: %s, agentapi_exe: %q, captured_at: %s}",
		c.ConversationID, c.LSAddress, redactedToken, c.AgentAPIExe, c.CapturedAt.Format(time.RFC3339))
}

// GoString is String for %#v, which would otherwise print the struct field by
// field and expose the token.
func (c AgyCreds) GoString() string { return c.String() }

// conversationIDRe is relevo's own copy of the agy conversation id rule: a
// lower-case 8-4-4-4-12 hex UUID. planner.Detect carries the same pattern for
// the environment it reads; both are pinned by tests.
var conversationIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// validConversationID reports whether conv is an agy conversation id.
func validConversationID(conv string) bool { return conversationIDRe.MatchString(conv) }

// loopbackAgyAddress reports whether addr is a host:port whose host is
// localhost or 127.0.0.1 and whose port is numeric. relevo refuses anything
// else: it would be sending a planner's report, which can contain source, to
// whatever host the value names.
func loopbackAgyAddress(addr string) bool {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host != "localhost" && host != "127.0.0.1" {
		return false
	}
	if _, err := strconv.Atoi(port); err != nil {
		return false
	}
	return true
}

// CaptureAgyCreds persists the calling agy session's agentapi credentials to
// dir/<conversation_id>.json and reports whether it wrote. Running inside agy
// is the only place the address and token exist, so every verb calls this once
// before dispatch: an agy planner runs relevo constantly (send, wait, pull,
// status), and the file is therefore fresh after every agy restart as soon as
// the planner next touches relevo.
//
// It writes nothing and returns false, nil unless a valid conversation id, a
// loopback address and a non-empty, whitespace-free token are all present, and
// skips the write when the file already carries the same address, token and
// exe. It never returns an error that contains the token: path errors name the
// path only.
func CaptureAgyCreds(env func(string) string, dir string, now time.Time) (bool, error) {
	if env == nil || dir == "" {
		return false, nil
	}

	conv := env(agyConversationEnv)
	if !validConversationID(conv) {
		return false, nil
	}
	addr := env(agyLSAddressEnv)
	if !loopbackAgyAddress(addr) {
		return false, nil
	}
	token := env(agyCSRFTokenEnv)
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return false, nil
	}

	creds := AgyCreds{
		ConversationID: conv,
		LSAddress:      addr,
		CSRFToken:      token,
		AgentAPIExe:    env(agyAgentAPIExeEnv),
		CapturedAt:     now,
	}

	if prev, err := ReadAgyCreds(dir, conv); err == nil &&
		prev.LSAddress == creds.LSAddress &&
		prev.CSRFToken == creds.CSRFToken &&
		prev.AgentAPIExe == creds.AgentAPIExe {
		return false, nil
	}

	if err := os.MkdirAll(dir, agyCredsDirMode); err != nil {
		return false, fmt.Errorf("agy credentials: create %s: %w", dir, err)
	}
	raw, err := json.Marshal(creds)
	if err != nil {
		return false, fmt.Errorf("agy credentials: encode: %w", err)
	}
	path := filepath.Join(dir, conv+".json")
	if err := writeAgyCredsFile(path, raw); err != nil {
		return false, fmt.Errorf("agy credentials: write %s: %w", path, err)
	}

	pruneAgyCreds(dir, now)
	return true, nil
}

// ReadAgyCreds reads and validates one conversation's credentials. A missing
// file returns an error wrapping os.ErrNotExist; the caller turns any error
// into "no credentials yet", so the distinction only matters to a reader.
func ReadAgyCreds(dir, conv string) (AgyCreds, error) {
	path := filepath.Join(dir, conv+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return AgyCreds{}, fmt.Errorf("agy credentials: read %s: %w", path, err)
	}
	var creds AgyCreds
	if err := json.Unmarshal(raw, &creds); err != nil {
		return AgyCreds{}, fmt.Errorf("agy credentials: decode %s: %w", path, err)
	}
	return creds, nil
}

// writeAgyCredsFile writes raw to path through a temp file in the same
// directory, so a concurrent reader never sees a half-written credential file,
// and renames it into place mode 0600.
func writeAgyCredsFile(path string, raw []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".agy-creds-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)

	if _, err := f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(agyCredsFileMode); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// pruneAgyCreds deletes every other credential file in dir whose capture is
// older than agyCredsPruneAfter. Best effort by design: it runs on the capture
// path, which must never fail a command, so every error is dropped.
func pruneAgyCreds(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var creds AgyCreds
		if err := json.Unmarshal(raw, &creds); err != nil {
			continue
		}
		if !creds.CapturedAt.IsZero() && now.Sub(creds.CapturedAt) > agyCredsPruneAfter {
			_ = os.Remove(path)
		}
	}
}
