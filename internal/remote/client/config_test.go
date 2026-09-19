package client

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestServersRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "servers.json")

	// Missing file -> empty, nil
	s, err := LoadServers(path)
	if err != nil {
		t.Fatalf("LoadServers on missing file: %v", err)
	}
	if len(s) != 0 {
		t.Fatalf("LoadServers on missing file returned %d entries, want 0", len(s))
	}

	// Save servers
	saved := Servers{
		"zen": ServerEntry{
			URL:         "https://zen:7777",
			Fingerprint: "sha256:abcd",
		},
		"local": ServerEntry{
			URL:      "http://localhost:8888",
			Insecure: true,
		},
	}
	if err := SaveServers(path, saved); err != nil {
		t.Fatalf("SaveServers: %v", err)
	}

	// Verify permissions (0600)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("permissions = %#o, want 0600", info.Mode().Perm())
	}

	// Load servers back
	loaded, err := LoadServers(path)
	if err != nil {
		t.Fatalf("LoadServers: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("len(loaded) = %d, want 2", len(loaded))
	}
	if loaded["zen"] != saved["zen"] {
		t.Fatalf("zen entry = %+v, want %+v", loaded["zen"], saved["zen"])
	}
	if loaded["local"] != saved["local"] {
		t.Fatalf("local entry = %+v, want %+v", loaded["local"], saved["local"])
	}
}

func TestValidateEntry(t *testing.T) {
	tests := []struct {
		name    string
		entry   ServerEntry
		wantErr bool
	}{
		{
			name: "valid https with fingerprint",
			entry: ServerEntry{
				URL:         "https://zen:7777",
				Fingerprint: "sha256:1234",
			},
			wantErr: false,
		},
		{
			name: "valid https with ca system",
			entry: ServerEntry{
				URL: "https://zen:7777",
				CA:  "system",
			},
			wantErr: false,
		},
		{
			name: "valid http with insecure",
			entry: ServerEntry{
				URL:      "http://localhost:8080",
				Insecure: true,
			},
			wantErr: false,
		},
		{
			name: "valid https with insecure",
			entry: ServerEntry{
				URL:      "https://localhost:8080",
				Insecure: true,
			},
			wantErr: false,
		},
		{
			name: "refuse http without insecure",
			entry: ServerEntry{
				URL: "http://zen:7777",
			},
			wantErr: true,
		},
		{
			name: "refuse https without pin or ca",
			entry: ServerEntry{
				URL: "https://zen:7777",
			},
			wantErr: true,
		},
		{
			name: "refuse invalid url",
			entry: ServerEntry{
				URL: "::not-a-url::",
			},
			wantErr: true,
		},
		{
			name: "refuse unsupported ca",
			entry: ServerEntry{
				URL: "https://zen:7777",
				CA:  "custom",
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateEntry(tc.entry)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateEntry(%+v) err = %v, wantErr = %v", tc.entry, err, tc.wantErr)
			}
		})
	}
}

func TestInitKeyAndLoadKey(t *testing.T) {
	configDir := t.TempDir()
	privPath, pubPath := KeyPaths(configDir)
	serversPath := ServersPath(configDir)

	if filepath.Base(privPath) != "client.key" || filepath.Base(pubPath) != "client.pub" {
		t.Fatalf("unexpected KeyPaths: %s, %s", privPath, pubPath)
	}
	if filepath.Base(serversPath) != "servers.json" {
		t.Fatalf("unexpected ServersPath: %s", serversPath)
	}

	// LoadKey when absent -> ErrNoKey
	_, err := LoadKey(privPath)
	if !errors.Is(err, ErrNoKey) {
		t.Fatalf("LoadKey on absent key got %v, want ErrNoKey", err)
	}

	// InitKey creates priv (0600) and pub (0644)
	kp, err := InitKey(privPath, pubPath)
	if err != nil {
		t.Fatalf("InitKey failed: %v", err)
	}

	privInfo, err := os.Stat(privPath)
	if err != nil {
		t.Fatalf("stat privPath: %v", err)
	}
	if privInfo.Mode().Perm() != 0o600 {
		t.Fatalf("privPath perm = %#o, want 0600", privInfo.Mode().Perm())
	}

	pubInfo, err := os.Stat(pubPath)
	if err != nil {
		t.Fatalf("stat pubPath: %v", err)
	}
	if pubInfo.Mode().Perm() != 0o644 {
		t.Fatalf("pubPath perm = %#o, want 0644", pubInfo.Mode().Perm())
	}

	// LoadKey loads the same keypair
	loaded, err := LoadKey(privPath)
	if err != nil {
		t.Fatalf("LoadKey: %v", err)
	}
	if !bytes.Equal(loaded.Private, kp.Private) {
		t.Fatalf("loaded private key does not match generated")
	}
	if !bytes.Equal(loaded.Public, kp.Public) {
		t.Fatalf("loaded public key does not match generated")
	}

	// InitKey refuses overwrite
	_, err = InitKey(privPath, pubPath)
	if !errors.Is(err, ErrKeyExists) {
		t.Fatalf("InitKey overwrite got %v, want ErrKeyExists", err)
	}
}
