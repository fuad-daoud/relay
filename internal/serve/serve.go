package serve

import (
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/git"
	"github.com/fuad-daoud/relay/internal/harness"
	"github.com/fuad-daoud/relay/internal/hooks"
	"github.com/fuad-daoud/relay/internal/policy"
	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/remote"
	"github.com/fuad-daoud/relay/internal/store"
	"github.com/fuad-daoud/relay/internal/usage"
)

type Config struct {
	Root           string // <state>/serve
	Candidates     *candidate.Set
	Policy         policy.Policy
	Runner         relay.Runner
	Git            *git.Client // concrete: the transport needs it too
	Now            func() time.Time
	Interval       time.Duration // daemon tick, floored by relay.NewDaemon
	MaxBundleBytes int64         // default 512 << 20
	Usage          usage.Reader  // nil = the server records "no reader", as today
	Prices         usage.Prices  // zero value = embedded defaults via usage.Fold's rules
	// StartedAt is when this relay serve process started; zero means
	// unknown, which disables the daemon-restart-relaunch check (#244).
	StartedAt time.Time
	// Roles checks candidate harness role-file coverage (#238); nil means
	// no check.
	Roles harness.RoleChecker
	// MaxBuilders caps headless builders running at once across all owners
	// (#285); 0 means cfg.Policy.MaxBuildersOrDefault().
	MaxBuilders int
	// Hooks dispatches lifecycle events (#285); nil means none, as today.
	Hooks hooks.Dispatcher
	// Scope is the systemd scope template served rounds launch under
	// (#244, #216); nil means no scopes -- cmdServeRun sets it from policy
	// only after ProbeScopes confirms systemd-run works on this box.
	Scope *relay.ScopeSpec
}

type Server struct {
	cfg          Config
	clients      *Clients
	nonces       *remote.NonceWindow  // ttl = remote.MaxClockSkew
	transport    remote.TreeTransport // remote.NewBundleTransport(cfg.Git, filepath.Join(cfg.Root, "tmp"))
	addr         net.Addr
	insecureHTTP bool
	mu           sync.Mutex // §6.3 of the spec: every store/ledger mutation and every tick
}

func New(cfg Config) (*Server, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.MaxBundleBytes <= 0 {
		cfg.MaxBundleBytes = 512 << 20
	}
	if cfg.Git == nil {
		cfg.Git = git.NewClient("git", 0, 0)
	}
	tmpDir := filepath.Join(cfg.Root, "tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return nil, err
	}
	clientsPath := filepath.Join(cfg.Root, "clients.json")
	clients, err := LoadClients(clientsPath)
	if err != nil {
		return nil, err
	}
	nonces := remote.NewNonceWindow(remote.MaxClockSkew)
	transport := remote.NewBundleTransport(cfg.Git, tmpDir)

	return &Server{
		cfg:       cfg,
		clients:   clients,
		nonces:    nonces,
		transport: transport,
	}, nil
}

func (s *Server) ownerRoot(owner remote.ClientID) (string, error) {
	dir, ok := owner.Dir()
	if !ok {
		return "", errors.New("malformed client id")
	}
	return filepath.Join(s.cfg.Root, "bindings", dir), nil
}

func (s *Server) repoRoot(owner remote.ClientID) (string, error) {
	dir, ok := owner.Dir()
	if !ok {
		return "", errors.New("malformed client id")
	}
	return filepath.Join(s.cfg.Root, "repos", dir), nil
}

// OwnerRuntime resolves owner's runtime: the same store-over-owner-dir
// runtime every server verb uses, exported for the ui's server source.
func (s *Server) OwnerRuntime(owner remote.ClientID) (relay.Runtime, error) {
	root, err := s.ownerRoot(owner)
	if err != nil {
		return relay.Runtime{}, err
	}
	return s.runtimeAt(root), nil
}

func (s *Server) runtime(id remote.ClientID) (relay.Runtime, error) {
	return s.OwnerRuntime(id)
}

func (s *Server) runtimeAt(root string) relay.Runtime {
	st := store.New(root)
	return relay.Runtime{
		Git:              s.cfg.Git,
		Runner:           s.cfg.Runner,
		Store:            st,
		Candidates:       s.cfg.Candidates,
		Policy:           s.cfg.Policy,
		LedgerPath:       filepath.Join(s.cfg.Root, "ledger.json"), // server-wide, not st.LedgerPath()
		AvailabilityPath: filepath.Join(s.cfg.Root, "availability.json"),
		Usage:            s.cfg.Usage,
		Prices:           s.cfg.Prices,
		Now:              s.cfg.Now,
		StartedAt:        s.cfg.StartedAt,
		Roles:            s.cfg.Roles,
		Hooks:            s.cfg.Hooks,
		Scope:            s.cfg.Scope,
		HeldCPUs:         func(tx *store.Tx, self string) ([]int, error) { return s.heldCPUs(root, tx, self) },
	}
}

// heldCPUs returns the cores held by live rounds other than self, across every
// owner's store (#314). It is the server's cross-owner census, injected as
// Runtime.HeldCPUs so internal/relay stays unaware of owners.
//
// The caller holds s.mu -- the same rule census and admit rely on (see
// admit.go): every tick and every admit is serialised by it, so taking another
// owner's store lock while holding the current owner's is safe, because the
// admin CLI takes one owner lock at a time.
//
// A per-owner List error is logged and skipped, exactly as census does, and
// the first is returned after the walk completes; the cores of the owners that
// did list are still returned.
func (s *Server) heldCPUs(root string, tx *store.Tx, self string) ([]int, error) {
	var held []int
	var firstErr error

	bindingsDir := filepath.Join(s.cfg.Root, "bindings")
	entries, err := os.ReadDir(bindingsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	for _, entry := range entries {
		id, ok := remote.IDFromDir(entry.Name())
		if !entry.IsDir() || !ok {
			slog.Warn("unexpected entry in bindings dir", "entry", entry.Name())
			continue
		}
		ownerPath := filepath.Join(bindingsDir, entry.Name())
		if ownerPath == root {
			// The current owner's bindings are already under the caller's tx.
			bindings, err := tx.List()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			held = append(held, relay.HeldIn(bindings, self)...)
			continue
		}
		bindings, err := store.New(ownerPath).List()
		if err != nil {
			slog.Warn("cpu census: list owner bindings failed", "owner", id, "err", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		// Another owner's binding can never be self.
		held = append(held, relay.HeldIn(bindings, "")...)
	}

	return held, firstErr
}
