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
	URL         string `json:"url"`
	Fingerprint string `json:"fingerprint,omitempty"` // sha256:<hex>
	CA          string `json:"ca,omitempty"`          // "" (pin) | "system"
	Insecure    bool   `json:"insecure,omitempty"`
}

type Servers map[string]ServerEntry // key: the server's short name

// ParseServers validates every entry, checking names in order so the first
// error is deterministic. It returns the map the JSON holds, never nil.
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

func EnrollLine(kp remote.Keypair) string {
	return remote.MarshalPublic(kp.Public, PublicComment())
}
