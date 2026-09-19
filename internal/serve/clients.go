package serve

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"log/slog"
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

// fileStamp records the ModTime and Size of clients.json at the last successful read;
// zero when the file was absent.
//
// Mtime resolution on some filesystems is one second; Size is in the stamp
// so two writes inside one second with different content still differ.
// Two writes inside one second with the same size would be missed, but
// enroll and revoke are human-paced operations.
type fileStamp struct {
	modTime time.Time
	size    int64
}

type Clients struct {
	path       string
	mu         sync.Mutex
	list       []Client
	stamp      fileStamp
	warnLogged map[string]bool
}

func (c *Clients) refresh() error {
	if c.path == "" {
		return nil
	}
	st, err := os.Stat(c.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if c.stamp != (fileStamp{}) {
				c.list = nil
				c.stamp = fileStamp{}
			}
			return nil
		}
		return err
	}

	if c.stamp != (fileStamp{}) && st.ModTime().Equal(c.stamp.modTime) && st.Size() == c.stamp.size {
		return nil
	}

	data, err := os.ReadFile(c.path)
	if err != nil {
		return err
	}
	var parsed []Client
	if err := json.Unmarshal(data, &parsed); err != nil {
		return err
	}
	c.list = parsed
	c.stamp = fileStamp{
		modTime: st.ModTime(),
		size:    st.Size(),
	}
	return nil
}

func (c *Clients) warnOnceLocked(err error) {
	if err == nil {
		return
	}
	msg := err.Error()
	if c.warnLogged == nil {
		c.warnLogged = make(map[string]bool)
	}
	if !c.warnLogged[msg] {
		c.warnLogged[msg] = true
		slog.Warn("refresh clients", "path", c.path, "err", err)
	}
}

func LoadClients(path string) (*Clients, error) {
	c := &Clients{path: path}
	if err := c.refresh(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Clients) Lookup(id remote.ClientID) (ed25519.PublicKey, remote.KeyStatus) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.refresh(); err != nil {
		c.warnOnceLocked(err)
	}

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

	if err := c.refresh(); err != nil {
		return Client{}, err
	}

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

	if err := c.refresh(); err != nil {
		return err
	}

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

	if err := c.refresh(); err != nil {
		c.warnOnceLocked(err)
	}

	out := make([]Client, len(c.list))
	copy(out, c.list)
	return out
}

func (c *Clients) LabelOf(id remote.ClientID) string {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.refresh(); err != nil {
		c.warnOnceLocked(err)
	}

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
	if err := os.Rename(tmpName, c.path); err != nil {
		return err
	}
	st, err := os.Stat(c.path)
	if err != nil {
		return err
	}
	c.stamp = fileStamp{
		modTime: st.ModTime(),
		size:    st.Size(),
	}
	return nil
}
