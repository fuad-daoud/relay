package client

import (
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/remote"
)

func TestServersRoundTrip(t *testing.T) {
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
	data, err := EncodeServers(saved)
	if err != nil {
		t.Fatalf("EncodeServers: %v", err)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Errorf("EncodeServers output does not end in a newline: %q", data)
	}

	loaded, err := ParseServers(data)
	if err != nil {
		t.Fatalf("ParseServers: %v", err)
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

// TestParseServersValidatesEntries pins that a stored servers body may not
// carry an entry ValidateEntry refuses.
func TestParseServersValidatesEntries(t *testing.T) {
	_, err := ParseServers([]byte(`{"bad":{"url":"http://zen:7777"}}`))
	if err == nil {
		t.Fatal("ParseServers accepted an http entry without insecure")
	}
	if !strings.Contains(err.Error(), "bad") {
		t.Errorf("error %v does not name the entry", err)
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

// TestEnrollLine pins the user@host comment rule lifted out of InitKey: the
// line is an "ed25519 <base64>" enrolment line carrying the same comment a
// written client.pub used to.
func TestEnrollLine(t *testing.T) {
	kp, err := remote.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	line := EnrollLine(kp)
	if !strings.HasPrefix(line, "ed25519 ") {
		t.Fatalf("EnrollLine = %q, want an ed25519 line", line)
	}
	if comment := PublicComment(); comment != "" {
		if !strings.HasSuffix(line, " "+comment) {
			t.Errorf("EnrollLine = %q, want suffix %q", line, " "+comment)
		}
	}

	pub, err := remote.ParsePublic(line)
	if err != nil {
		t.Fatalf("ParsePublic(EnrollLine): %v", err)
	}
	if remote.IDOf(pub) != remote.IDOf(kp.Public) {
		t.Errorf("enrolment line names a different key")
	}
}
