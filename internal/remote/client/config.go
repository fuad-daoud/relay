package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/user"
	"sort"

	"github.com/fuad-daoud/relevo/internal/remote"
)

var (
	ErrNoKey     = errors.New("no client key; run relevo config server key")
	ErrKeyExists = errors.New("client key already exists")
)

type ServerEntry struct {
	URL         string `json:"url"`                   // https://zen:7777 (or http:// only with Insecure)
	Fingerprint string `json:"fingerprint,omitempty"` // sha256:<hex>; required unless CA == "system"
	CA          string `json:"ca,omitempty"`          // "" (pin) | "system"
	Insecure    bool   `json:"insecure,omitempty"`    // plain http allowed
}

type Servers map[string]ServerEntry // key: the server's short name

// ParseServers decodes a servers map from data and validates every entry with
// ValidateEntry. Entries are checked in name order, so the first error is
// deterministic. A missing body is the caller's concern; this returns exactly
// the map the JSON holds, never nil.
func ParseServers(data []byte) (Servers, error) {
	var s Servers
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse servers file: %w", err)
	}
	if s == nil {
		s = make(Servers)
	}

	names := make([]string, 0, len(s))
	for name := range s {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := ValidateEntry(s[name]); err != nil {
			return nil, fmt.Errorf("parse servers file: %s: %w", name, err)
		}
	}
	return s, nil
}

// EncodeServers renders s as the indented JSON bodies the servers section
// stores, with a trailing newline. It is the inverse of ParseServers.
func EncodeServers(s Servers) ([]byte, error) {
	if s == nil {
		s = make(Servers)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal servers: %w", err)
	}
	return append(data, '\n'), nil
}

// ValidateEntry checks that URL parses, is https unless Insecure, and has fingerprint or CA unless Insecure.
func ValidateEntry(e ServerEntry) error {
	u, err := url.Parse(e.URL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return errors.New("invalid url")
	}
	if !e.Insecure {
		if u.Scheme != "https" {
			return errors.New("https required unless insecure")
		}
		if e.Fingerprint == "" && e.CA == "" {
			return errors.New("fingerprint or ca required for https")
		}
		if e.CA != "" && e.CA != "system" {
			return errors.New("ca must be 'system' or empty")
		}
	} else {
		if u.Scheme != "http" && u.Scheme != "https" {
			return errors.New("http or https required")
		}
	}
	return nil
}

// PublicComment is the "user@host" comment an enrolment line carries, the
// rule InitKey used before the key moved into the database. It is "" when the
// username cannot be read.
func PublicComment() string {
	u, err := user.Current()
	if err != nil || u.Username == "" {
		return ""
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		return u.Username + "@" + h
	}
	return u.Username + "@localhost"
}

// EnrollLine renders kp's public key as the enrolment line a server admin runs
// `relevo serve enroll --key "<line>"` with, with the default comment. Callers
// derive it from the key secret instead of reading a client.pub file.
func EnrollLine(kp remote.Keypair) string {
	return remote.MarshalPublic(kp.Public, PublicComment())
}
