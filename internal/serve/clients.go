package serve

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fuad-daoud/relay/internal/remote"
)

var (
	ErrAlreadyEnrolled = errors.New("client already enrolled")
	ErrNoSuchClient    = errors.New("no such client")
)

type Client struct {
	ID         remote.ClientID `json:"id"`
	Label      string          `json:"label"`
	PubKey     string          `json:"pubkey"` // the enrollment line, remote.MarshalPublic form
	EnrolledAt time.Time       `json:"enrolled_at"`
	RevokedAt  time.Time       `json:"revoked_at,omitempty"`
}

type Clients struct {
	path string
	mu   sync.Mutex
	list []Client
}

func LoadClients(path string) (*Clients, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Clients{path: path, list: []Client{}}, nil
		}
		return nil, err
	}
	var list []Client
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	return &Clients{
		path: path,
		list: list,
	}, nil
}

func (c *Clients) Lookup(id remote.ClientID) (ed25519.PublicKey, remote.KeyStatus) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, cl := range c.list {
		if cl.ID == id {
			if !cl.RevokedAt.IsZero() {
				return nil, remote.KeyRevoked
			}
			pub, err := remote.ParsePublic(cl.PubKey)
			if err != nil {
				return nil, remote.KeyUnknown
			}
			return pub, remote.KeyActive
		}
	}
	return nil, remote.KeyUnknown
}

func (c *Clients) Add(label, pubLine string, now time.Time) (Client, error) {
	pub, err := remote.ParsePublic(pubLine)
	if err != nil {
		return Client{}, err
	}
	id := remote.IDOf(pub)

	c.mu.Lock()
	defer c.mu.Unlock()

	for i, cl := range c.list {
		if cl.ID == id {
			if cl.RevokedAt.IsZero() {
				return Client{}, ErrAlreadyEnrolled
			}
			// re-enrolling a revoked id clears RevokedAt
			c.list[i].RevokedAt = time.Time{}
			if label != "" {
				c.list[i].Label = label
			}
			c.list[i].PubKey = pubLine
			if err := c.saveLocked(); err != nil {
				return Client{}, err
			}
			return c.list[i], nil
		}
	}

	cl := Client{
		ID:         id,
		Label:      label,
		PubKey:     pubLine,
		EnrolledAt: now,
	}
	c.list = append(c.list, cl)
	if err := c.saveLocked(); err != nil {
		return Client{}, err
	}
	return cl, nil
}

func (c *Clients) Revoke(id remote.ClientID, now time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	for i, cl := range c.list {
		if cl.ID == id {
			c.list[i].RevokedAt = now
			return c.saveLocked()
		}
	}
	return ErrNoSuchClient
}

func (c *Clients) List() []Client {
	c.mu.Lock()
	defer c.mu.Unlock()

	out := make([]Client, len(c.list))
	copy(out, c.list)
	return out
}

func (c *Clients) LabelOf(id remote.ClientID) string {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, cl := range c.list {
		if cl.ID == id && cl.Label != "" {
			return cl.Label
		}
	}
	s := string(id)
	s = strings.TrimPrefix(s, "SHA256:")
	if len(s) > 8 {
		s = s[:8]
	}
	return s
}

func (c *Clients) saveLocked() error {
	if c.path == "" {
		return nil
	}
	dir := filepath.Dir(c.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c.list, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "clients-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, c.path)
}
