package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/user"
	"path/filepath"

	"github.com/fuad-daoud/relevo/internal/remote"
)

var (
	ErrNoKey     = errors.New("no client key; run relevo client init")
	ErrKeyExists = errors.New("client key already exists")
)

type ServerEntry struct {
	URL         string `json:"url"`                   // https://zen:7777 (or http:// only with Insecure)
	Fingerprint string `json:"fingerprint,omitempty"` // sha256:<hex>; required unless CA == "system"
	CA          string `json:"ca,omitempty"`          // "" (pin) | "system"
	Insecure    bool   `json:"insecure,omitempty"`    // plain http allowed
}

type Servers map[string]ServerEntry // key: the server's short name

// LoadServers loads the servers map from path. A missing file returns an empty map and nil error.
func LoadServers(path string) (Servers, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return make(Servers), nil
		}
		return nil, fmt.Errorf("read servers file: %w", err)
	}
	var s Servers
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse servers file: %w", err)
	}
	if s == nil {
		s = make(Servers)
	}
	return s, nil
}

// SaveServers saves the servers map to path atomically (write temp and rename) with permissions 0600.
func SaveServers(path string, s Servers) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal servers: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(dir, "servers-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temp file: %w", err)
	}
	tmpName = ""
	return nil
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

// KeyPaths returns the private and public key paths for a given configDir.
func KeyPaths(configDir string) (priv, pub string) {
	return filepath.Join(configDir, "relevo", "client.key"), filepath.Join(configDir, "relevo", "client.pub")
}

// ServersPath returns the servers.json path for a given configDir.
func ServersPath(configDir string) string {
	return filepath.Join(configDir, "relevo", "servers.json")
}

// LoadKey loads the ed25519 private key from privPath. Returns ErrNoKey when absent.
func LoadKey(privPath string) (remote.Keypair, error) {
	data, err := os.ReadFile(privPath)
	if err != nil {
		if os.IsNotExist(err) {
			return remote.Keypair{}, ErrNoKey
		}
		return remote.Keypair{}, fmt.Errorf("read client key: %w", err)
	}
	kp, err := remote.ParsePrivate(data)
	if err != nil {
		return remote.Keypair{}, fmt.Errorf("parse client key: %w", err)
	}
	return kp, nil
}

// InitKey generates a fresh ed25519 keypair and writes privPath (0600) and pubPath (0644).
// Returns ErrKeyExists when privPath or pubPath already exists.
func InitKey(privPath, pubPath string) (remote.Keypair, error) {
	if _, err := os.Stat(privPath); err == nil {
		return remote.Keypair{}, ErrKeyExists
	}
	if _, err := os.Stat(pubPath); err == nil {
		return remote.Keypair{}, ErrKeyExists
	}

	dir := filepath.Dir(privPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return remote.Keypair{}, fmt.Errorf("create directory: %w", err)
	}
	pubDir := filepath.Dir(pubPath)
	if pubDir != dir {
		if err := os.MkdirAll(pubDir, 0o700); err != nil {
			return remote.Keypair{}, fmt.Errorf("create public key directory: %w", err)
		}
	}

	kp, err := remote.Generate()
	if err != nil {
		return remote.Keypair{}, fmt.Errorf("generate keypair: %w", err)
	}

	privPEM, err := remote.MarshalPrivate(kp)
	if err != nil {
		return remote.Keypair{}, fmt.Errorf("marshal private key: %w", err)
	}

	comment := ""
	if u, err := user.Current(); err == nil && u.Username != "" {
		if h, err := os.Hostname(); err == nil && h != "" {
			comment = u.Username + "@" + h
		} else {
			comment = u.Username + "@localhost"
		}
	}
	pubLine := remote.MarshalPublic(kp.Public, comment) + "\n"

	// Write privPath exclusively (0600)
	privFile, err := os.OpenFile(privPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return remote.Keypair{}, ErrKeyExists
		}
		return remote.Keypair{}, fmt.Errorf("write private key: %w", err)
	}
	if _, err := privFile.Write(privPEM); err != nil {
		_ = privFile.Close()
		_ = os.Remove(privPath)
		return remote.Keypair{}, fmt.Errorf("write private key: %w", err)
	}
	if err := privFile.Close(); err != nil {
		_ = os.Remove(privPath)
		return remote.Keypair{}, fmt.Errorf("close private key: %w", err)
	}

	// Write pubPath exclusively (0644)
	pubFile, err := os.OpenFile(pubPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		_ = os.Remove(privPath)
		if os.IsExist(err) {
			return remote.Keypair{}, ErrKeyExists
		}
		return remote.Keypair{}, fmt.Errorf("write public key: %w", err)
	}
	if _, err := pubFile.Write([]byte(pubLine)); err != nil {
		_ = pubFile.Close()
		_ = os.Remove(privPath)
		_ = os.Remove(pubPath)
		return remote.Keypair{}, fmt.Errorf("write public key: %w", err)
	}
	if err := pubFile.Close(); err != nil {
		_ = os.Remove(privPath)
		_ = os.Remove(pubPath)
		return remote.Keypair{}, fmt.Errorf("close public key: %w", err)
	}

	return kp, nil
}
