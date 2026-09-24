package serve

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/hooks"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
)

type Config struct {
	Root string // <state>/serve
	// DB is the machine database (store.DefaultRoot()/relevo.db), which the
	// caller opens and closes (P5 §4.3). The server reads and writes every
	// binding record, client, secret and gate through it and no longer opens a
	// database of its own under Root.
	DB             *db.DB
	Candidates     *candidate.Set
	Policy         policy.Policy
	Runner         relevo.Runner
	Git            *git.Client // concrete: the transport needs it too
	Now            func() time.Time
	Interval       time.Duration // daemon tick, floored by relevo.NewDaemon
	MaxBundleBytes int64         // default 512 << 20
	Usage          usage.Reader  // nil = the server records "no reader", as today
	Prices         usage.Prices  // zero value = embedded defaults via usage.Fold's rules
	// StartedAt is when this relevo serve process started; zero means
	// unknown, which disables the daemon-restart-relaunch check (#244).
	StartedAt time.Time
	// Roles checks candidate harness role-file coverage (#238); nil means
	// no check.
	Roles harness.RoleChecker
	// Registry is relevo's roles registry (#374): roles.json merged over the
	// built-ins, or the legacy derivation. Nil means the runtime derives it
	// from Candidates and Policy on demand.
	Registry *roles.Registry
	// MaxBuilders caps headless builders running at once across all owners
	// (#285); 0 means cfg.Policy.MaxBuildersOrDefault().
	MaxBuilders int
	// Hooks dispatches lifecycle events (#285); nil means none, as today.
	Hooks hooks.Dispatcher
	// Scope is the systemd scope template served rounds launch under
	// (#244, #216); nil means no scopes -- cmdServeRun sets it from policy
	// only after ProbeScopes confirms systemd-run works on this box.
	Scope *relevo.ScopeSpec
}

type Server struct {
	cfg          Config
	clients      *Clients
	nonces       *remote.NonceWindow  // ttl = remote.MaxClockSkew
	transport    remote.TreeTransport // remote.NewBundleTransport(cfg.Git, filepath.Join(cfg.Root, "tmp"))
	addr         net.Addr
	insecureHTTP bool
	mu           sync.Mutex // §6.3 of the spec: every store/ledger mutation and every tick
	// gates is the server-wide gate and availability store: a `serve.`-prefixed
	// view of the machine database (P5 §4.3), so a server-wide gate never
	// collides with this machine's own `ledger` and `availability` rows. It is
	// built once here and shared by every runtime the server builds.
	gates db.KV
	// stores is one Store per owner root (P5 §4.3): every record call goes
	// through the machine database, so a tick opens no per-owner handle. It is
	// guarded by s.mu, which every caller of ownerStore holds.
	stores map[string]*store.Store
	// liveCache caches running round LiveViews per caller/binding/round.
	liveCache *liveCache
	// tickFn, when non-nil, replaces Tick in Run (#373): the drain and
	// WithoutCancel tests need a tick they can hold open. Production leaves
	// it nil.
	tickFn func(context.Context) error
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
	// Serve's startup path, before anything listens: drop the request temp
	// files a previous process left behind (#373 §4.3). A sweep error is a
	// Warn and startup continues.
	if removed, err := sweepTmp(tmpDir, time.Hour, cfg.Now()); err != nil {
		slog.Warn("temp sweep failed", "dir", tmpDir, "removed", removed, "err", err)
	}
	clientsPath := filepath.Join(cfg.Root, "clients.json")
	clients, err := LoadClients(cfg.DB, clientsPath)
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
		gates:     db.PrefixKV{KV: cfg.DB, Prefix: "serve."},
		stores:    map[string]*store.Store{},
		liveCache: newLiveCache(),
	}, nil
}

// DB returns the machine database the server holds (P5 §4.3): the store served
// rounds and the admin verbs share, and the one the serve ui keeps its own
// preferences in.
func (s *Server) DB() *db.DB { return s.cfg.DB }

// ownerIDOf is the enrolled client id a bindings directory names: its basename
// is the id's hex form (remote.ClientID.Dir), and IDFromDir turns it back.
func ownerIDOf(root string) remote.ClientID {
	id, _ := remote.IDFromDir(filepath.Base(root))
	return id
}

// ownerStore returns the one Store for an owner root, creating it on first use
// (P5 §4.3). Every record call goes through the machine database, scoped to the
// owner the root names, so the server opens no per-owner handle and no file
// under the owner's directory. The caller holds s.mu.
func (s *Server) ownerStore(root string) *store.Store {
	if st, ok := s.stores[root]; ok {
		return st
	}
	st := store.NewShared(root, string(ownerIDOf(root)), s.cfg.DB)
	s.stores[root] = st
	return st
}

// sweepTmp removes the stale request temp files in dir (#373 §4.3): the
// req-body-* and plan-* files relevo serve creates while a request is in
// flight, whose mtime is older than olderThan. It matches no other name. It is
// pure apart from the filesystem -- now is a parameter, so a test pins the
// age boundary. A missing dir is empty, not an error; a per-file failure is
// kept as the first error and the walk continues, so one unreadable file
// cannot stop the sweep. removed counts the files actually removed.
func sweepTmp(dir string, olderThan time.Duration, now time.Time) (removed int, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}

	var firstErr error
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "req-body-") && !strings.HasPrefix(name, "plan-") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if now.Sub(info.ModTime()) < olderThan {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		removed++
	}
	return removed, firstErr
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
// runtime every server verb uses, exported for the ui's server source. It
// takes s.mu itself, because the ui reaches it without a lock; a caller that
// already holds s.mu calls runtimeAt directly.
func (s *Server) OwnerRuntime(owner remote.ClientID) (relevo.Runtime, error) {
	root, err := s.ownerRoot(owner)
	if err != nil {
		return relevo.Runtime{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runtimeAt(root), nil
}

// runtime is OwnerRuntime for a caller that already holds s.mu.
func (s *Server) runtime(id remote.ClientID) (relevo.Runtime, error) {
	root, err := s.ownerRoot(id)
	if err != nil {
		return relevo.Runtime{}, err
	}
	return s.runtimeAt(root), nil
}

func (s *Server) runtimeAt(root string) relevo.Runtime {
	st := s.ownerStore(root)
	return relevo.Runtime{
		Git:        s.cfg.Git,
		Runner:     s.cfg.Runner,
		Store:      st,
		Candidates: s.cfg.Candidates,
		Policy:     s.cfg.Policy,
		Gates:      s.gates,    // server-wide, not the owner's own
		GatesDir:   s.cfg.Root, // its legacy ledger.json/availability.json/history.json
		Usage:      s.cfg.Usage,
		Prices:     s.cfg.Prices,
		Now:        s.cfg.Now,
		StartedAt:  s.cfg.StartedAt,
		Roles:      s.cfg.Roles,
		Registry:   s.cfg.Registry,
		Hooks:      s.cfg.Hooks,
		Scope:      s.cfg.Scope,
		HeldCPUs:   func(tx *store.Tx, self string) ([]int, error) { return s.heldCPUs(root, tx, self) },
	}
}

// heldCPUs returns the cores held by live rounds other than self, across every
// owner's store (#314). It is the server's cross-owner census, injected as
// Runtime.HeldCPUs so internal/relevo stays unaware of owners.
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
			held = append(held, relevo.HeldIn(bindings, self)...)
			continue
		}
		bindings, err := s.ownerStore(ownerPath).List()
		if err != nil {
			slog.Warn("cpu census: list owner bindings failed", "owner", id, "err", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		// Another owner's binding can never be self.
		held = append(held, relevo.HeldIn(bindings, "")...)
	}

	return held, firstErr
}
