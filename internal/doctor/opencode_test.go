package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestOpencodeServiceCheck pins the 2.x shared-service note (#256): the row
// appears only when service.json exists, is always SevOK (it reports a fact
// about how opencode 2.x runs, not a misconfiguration), and its session
// count comes from opencode.db through sqlite3, not the service's HTTP API.
func TestOpencodeServiceCheck(t *testing.T) {
	const svcPath = "/fake/home/.config/opencode/service.json"
	const dbPath = "/fake/home/.local/share/opencode/opencode.db"

	t.Run("no service.json is silent", func(t *testing.T) {
		env := &fakeEnv{homeDir: "/fake/home"}
		c := opencodeServiceCheck(context.Background(), env)
		if c.Name != "" {
			t.Errorf("check = %+v, want a zero-value row when service.json is absent", c)
		}
	})

	t.Run("service.json present notes the shared service", func(t *testing.T) {
		env := &fakeEnv{homeDir: "/fake/home", existingFiles: map[string]bool{svcPath: true}}
		c := opencodeServiceCheck(context.Background(), env)
		if c.Group != "opencode" || c.Name != "service" || c.Severity != SevOK {
			t.Errorf("row = %+v, want an OK opencode/service row", c)
		}
		if !strings.Contains(c.Detail, "2.x shared service") {
			t.Errorf("Detail = %q, want it to name the shared service", c.Detail)
		}
	})

	t.Run("opencode.db present adds the session count", func(t *testing.T) {
		env := &fakeEnv{
			homeDir:       "/fake/home",
			existingFiles: map[string]bool{svcPath: true, dbPath: true},
			commandOut:    []byte("3\n"),
		}
		c := opencodeServiceCheck(context.Background(), env)
		if !strings.Contains(c.Detail, "3 session(s)") {
			t.Errorf("Detail = %q, want the session count read from opencode.db", c.Detail)
		}
	})

	t.Run("no opencode.db leaves only the base note", func(t *testing.T) {
		env := &fakeEnv{homeDir: "/fake/home", existingFiles: map[string]bool{svcPath: true}}
		c := opencodeServiceCheck(context.Background(), env)
		if strings.Contains(c.Detail, "session(s)") {
			t.Errorf("Detail = %q, want no session count with no opencode.db", c.Detail)
		}
	})

	t.Run("a failing sqlite3 query still returns the base note", func(t *testing.T) {
		env := &fakeEnv{
			homeDir:       "/fake/home",
			existingFiles: map[string]bool{svcPath: true, dbPath: true},
			commandErr:    errors.New("sqlite3: not found"),
		}
		c := opencodeServiceCheck(context.Background(), env)
		if c.Severity != SevOK || !strings.Contains(c.Detail, "2.x shared service") {
			t.Errorf("check = %+v, want the base note even when the count fails", c)
		}
		if strings.Contains(c.Detail, "session(s)") {
			t.Errorf("Detail = %q, want no session count when the query fails", c.Detail)
		}
	})
}

// TestOpencodeAllowlistCheck pins the opencode config check (#236): a headless
// opencode builder cannot read its plan under relay's state root unless
// permission.external_directory allows that directory, and opencode's config
// is JSONC -- comments and trailing commas are part of the file, not a fault.
func TestOpencodeAllowlistCheck(t *testing.T) {
	const stateRoot = "/fake/home/.local/state/relay"
	const jsoncPath = "/fake/home/.config/opencode/opencode.jsonc"
	const jsonPath = "/fake/home/.config/opencode/opencode.json"

	// newEnv is a fakeEnv rooted at /fake/home with at most one opencode
	// config file present.
	newEnv := func(t *testing.T, path, content string) *fakeEnv {
		t.Helper()
		env := &fakeEnv{
			homeDir:       "/fake/home",
			existingFiles: map[string]bool{},
			fileContents:  map[string]string{},
		}
		if path != "" {
			env.existingFiles[path] = true
			env.fileContents[path] = content
		}
		return env
	}

	t.Run("an allow entry for the state root is ok", func(t *testing.T) {
		env := newEnv(t, jsoncPath, `{"permission":{"external_directory":{"/fake/home/.local/state/relay/**":"allow"}}}`)
		c := opencodeAllowlistCheck(env, stateRoot)
		if c.Group != "opencode" || c.Name != "external_directory" {
			t.Errorf("row = %+v, want the opencode external_directory row", c)
		}
		if c.Severity != SevOK || c.Detail != jsoncPath+" allows "+stateRoot+"/**" {
			t.Errorf("check = %+v, want SevOK naming the file that allows the state root", c)
		}
	})

	t.Run("comments and a trailing comma still parse", func(t *testing.T) {
		env := newEnv(t, jsoncPath, `{
  // opencode reads this file with comments in it
  "permission": {
    "external_directory": {
      "/fake/home/.local/state/relay/**": "allow", /* relay stages plans here */
    },
  },
}`)
		if c := opencodeAllowlistCheck(env, stateRoot); c.Severity != SevOK {
			t.Errorf("check = %+v, want SevOK: stripJSONC must drop // and /* */ and a trailing comma", c)
		}
	})

	t.Run("no permission key warns with the snippet", func(t *testing.T) {
		env := newEnv(t, jsoncPath, `{"model":"some/model"}`)
		c := opencodeAllowlistCheck(env, stateRoot)
		if c.Severity != SevWarn {
			t.Errorf("severity = %v, want warn", c.Severity)
		}
		if !strings.Contains(c.Fix, stateRoot+"/**") || !strings.Contains(c.Fix, `"allow"`) {
			t.Errorf("Fix = %q, want the README allowlist snippet naming the state root", c.Fix)
		}
	})

	t.Run("an ask entry does not allow", func(t *testing.T) {
		env := newEnv(t, jsoncPath, `{"permission":{"external_directory":{"/fake/home/.local/state/relay/**":"ask"}}}`)
		if c := opencodeAllowlistCheck(env, stateRoot); c.Severity != SevWarn {
			t.Errorf("severity = %v, want warn: only allow opens the directory", c.Severity)
		}
	})

	t.Run("a parent glob covers the state root", func(t *testing.T) {
		env := newEnv(t, jsoncPath, `{"permission":{"external_directory":{"/fake/home/.local/state/**":"allow"}}}`)
		if c := opencodeAllowlistCheck(env, stateRoot); c.Severity != SevOK {
			t.Errorf("check = %+v, want SevOK from a parent glob", c)
		}
	})

	t.Run("the state root itself with allow is enough", func(t *testing.T) {
		env := newEnv(t, jsoncPath, `{"permission":{"external_directory":{"/fake/home/.local/state/relay":"allow"}}}`)
		if c := opencodeAllowlistCheck(env, stateRoot); c.Severity != SevOK {
			t.Errorf("check = %+v, want SevOK from an exact state-root key", c)
		}
	})

	t.Run("no config at all", func(t *testing.T) {
		env := newEnv(t, "", "")
		c := opencodeAllowlistCheck(env, stateRoot)
		if c.Severity != SevWarn || !strings.Contains(c.Detail, "no ~/.config/opencode/opencode.jsonc") {
			t.Errorf("check = %+v, want a warn naming the missing jsonc", c)
		}
	})

	t.Run("opencode.json without the c is read too", func(t *testing.T) {
		env := newEnv(t, jsonPath, `{"permission":{"external_directory":{"/fake/home/.local/state/relay/*":"allow"}}}`)
		if c := opencodeAllowlistCheck(env, stateRoot); c.Severity != SevOK {
			t.Errorf("check = %+v, want SevOK from opencode.json", c)
		}
	})

	t.Run("the jsonc candidate wins over the json one", func(t *testing.T) {
		env := newEnv(t, jsoncPath, `{"permission":{"external_directory":{"/fake/home/.local/state/relay/**":"allow"}}}`)
		env.existingFiles[jsonPath] = true
		env.fileContents[jsonPath] = `{}`
		c := opencodeAllowlistCheck(env, stateRoot)
		if c.Severity != SevOK || !strings.Contains(c.Detail, jsoncPath) {
			t.Errorf("check = %+v, want the first readable candidate to win", c)
		}
	})

	t.Run("a config that does not parse warns with the error", func(t *testing.T) {
		env := newEnv(t, jsoncPath, `{"permission": `)
		c := opencodeAllowlistCheck(env, stateRoot)
		if c.Severity != SevWarn || !strings.Contains(c.Detail, "could not parse "+jsoncPath) {
			t.Errorf("check = %+v, want a warn naming the unparseable file", c)
		}
		if !strings.Contains(c.Fix, stateRoot+"/**") {
			t.Errorf("Fix = %q, want the allowlist snippet", c.Fix)
		}
	})
}

func TestStripJSONC(t *testing.T) {
	// A URL inside a string literal is content, not a comment.
	in := []byte(`{"url":"http://x"}`)
	if got := string(stripJSONC(in)); !strings.Contains(got, `"http://x"`) {
		t.Errorf("stripJSONC(%s) = %s, want the string literal intact", in, got)
	}

	// An escaped quote does not end the literal.
	got := string(stripJSONC([]byte(`{"k":"a \" // not a comment"}`)))
	if !strings.Contains(got, `a \" // not a comment`) {
		t.Errorf("stripJSONC() = %s, want the escaped literal intact", got)
	}

	// Comments and a trailing comma both go.
	got = string(stripJSONC([]byte("{\n// c\n\"a\": 1,\n/* d */\n}")))
	if strings.Contains(got, "//") || strings.Contains(got, "/*") {
		t.Errorf("stripJSONC() = %s, want the comments removed", got)
	}
	if !strings.Contains(got, `"a": 1`) || strings.Contains(got, "1,") {
		t.Errorf("stripJSONC() = %s, want the trailing comma removed", got)
	}
}
