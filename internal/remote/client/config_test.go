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
		{"valid https with fingerprint", ServerEntry{URL: "https://zen:7777", Fingerprint: "sha256:1234"}, false},
		{"valid https with ca system", ServerEntry{URL: "https://zen:7777", CA: "system"}, false},
		{"valid http with insecure", ServerEntry{URL: "http://localhost:8080", Insecure: true}, false},
		{"valid https with insecure", ServerEntry{URL: "https://localhost:8080", Insecure: true}, false},
		{"refuse http without insecure", ServerEntry{URL: "http://zen:7777"}, true},
		{"refuse https without pin or ca", ServerEntry{URL: "https://zen:7777"}, true},
		{"refuse invalid url", ServerEntry{URL: "::not-a-url::"}, true},
		{"refuse unsupported ca", ServerEntry{URL: "https://zen:7777", CA: "custom"}, true},
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
