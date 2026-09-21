package serve

import (
	"errors"
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
		Herdr:            stubHerdr{},
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
	}
}
