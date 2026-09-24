package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/proc"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/serve"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/ui"
)

type hostSlice []string

func (h *hostSlice) String() string {
	return strings.Join(*h, ",")
}

func (h *hostSlice) Set(val string) error {
	*h = append(*h, val)
	return nil
}

type serveFlags struct {
	listen         string
	state          string
	interval       time.Duration
	insecureHTTP   bool
	maxBundleBytes int64
	maxBuilders    int
}

func serveFlagSet() (*flag.FlagSet, *serveFlags) {
	fs := flag.NewFlagSet("relevo serve", flag.ContinueOnError)
	var sf serveFlags
	fs.StringVar(&sf.listen, "listen", ":7777", "listen address")
	fs.StringVar(&sf.state, "state", "", "state directory (defaults to $XDG_STATE_HOME/relevo)")
	fs.DurationVar(&sf.interval, "interval", 2*time.Second, "poll interval")
	fs.BoolVar(&sf.insecureHTTP, "insecure-http", false, "serve plain HTTP without TLS")
	fs.Int64Var(&sf.maxBundleBytes, "max-bundle-bytes", 512<<20, "maximum bundle size in bytes")
	fs.IntVar(&sf.maxBuilders, "max-builders", 0, "headless builders running at once across all owners (0 = policy.json serve.max_builders, else max(1, NumCPU-1))")
	return fs, &sf
}

func serveRoot(fs *flag.FlagSet) (string, error) {
	var stateDir string
	if f := fs.Lookup("state"); f != nil {
		stateDir = f.Value.String()
	}
	if stateDir != "" {
		return filepath.Join(stateDir, "serve"), nil
	}
	return defaultServeRoot()
}

func defaultServeRoot() (string, error) {
	root, err := store.DefaultRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "serve"), nil
}

func pidAlive(pid int) bool {
	// Windows does not support the Unix signal-zero liveness probe, so daemon
	// pointers are ignored there and the default root is used.
	if runtime.GOOS == "windows" {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

func adminRoot(fs *flag.FlagSet) (string, error) {
	var state string
	if f := fs.Lookup("state"); f != nil {
		state = f.Value.String()
	}
	def, err := defaultServeRoot()
	if err != nil {
		return "", err
	}
	root, note, err := serve.ResolveAdminRoot(state, def, pidAlive)
	if err != nil {
		return "", err
	}
	if note != "" {
		fmt.Fprintf(os.Stderr, "relevo serve: %s\n", note)
	}
	initialised, err := serve.Initialised(root)
	if err != nil {
		return "", err
	}
	if !initialised {
		return "", fmt.Errorf("no serve state at %s: run relevo serve init, or pass --state <dir> matching the daemon's", root)
	}
	return root, nil
}

func cmdServe(args []string) error {
	const usage = `usage: relevo serve [--listen :7777] [--state <dir>] [--interval 2s] [--insecure-http] [--max-bundle-bytes N] [--max-builders N]
       relevo serve init [--host <name>]... [--state <dir>]
       relevo serve enroll --label <label> --key "<ed25519 line>" [--state <dir>]
       relevo serve clients [--state <dir>]
       relevo serve revoke <id> [--state <dir>]
       relevo serve fingerprint [--state <dir>]
       relevo serve status [--state <dir>]
       relevo serve log --owner <label|id> <name> [--round N] [--after N] [--json] [--follow] [--state <dir>]
       relevo serve show --owner <label|id> <name> [--round N] [--plan|--report|--diff|--drift|--log|--transcript] [--json] [--state <dir>]
       relevo serve tab [--owner <label|id>] [--since 7d] [--by binding|model|provider|owner] [--json] [--state <dir>]
       relevo gate --serve [--state <dir>]        (gates, available and unavailable moved to relevo gate)
       relevo serve ui [--state <dir>] [--interval 2s]
       relevo serve gc --abandoned <duration> [--dry-run] [--state <dir>]
       relevo serve unbind --owner <label|id> <name> [--state <dir>] [--force]`

	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return exitCodeErr{code: 2}
	}

	switch args[0] {
	case "init":
		return cmdServeInit(args[1:])
	case "enroll":
		return cmdServeEnroll(args[1:])
	case "clients":
		return cmdServeClients(args[1:])
	case "revoke":
		return cmdServeRevoke(args[1:])
	case "fingerprint":
		return cmdServeFingerprint(args[1:])
	case "status":
		return cmdServeStatus(args[1:])
	case "log":
		return cmdServeLog(args[1:])
	case "show":
		return cmdServeShow(args[1:])
	case "tab":
		return cmdServeTab(args[1:])
	case "gates", "available", "unavailable":
		fmt.Fprintf(os.Stderr, "relevo serve %s was removed; use relevo gate --serve …\n", args[0])
		return exitCodeErr{code: 2}
	case "ui":
		return cmdServeUI(args[1:])
	case "gc":
		return cmdServeGC(args[1:])
	case "unbind":
		return cmdServeUnbind(args[1:])
	case "help", "-h", "--help":
		fmt.Println(usage)
		return nil
	default:
		if strings.HasPrefix(args[0], "-") {
			return cmdServeRun(args)
		}
		fmt.Fprintf(os.Stderr, "relevo serve: unknown command %q\n", args[0])
		return exitCodeErr{code: 2}
	}
}

// serveTierRuntime is the Runtime cmdServeRun uses only to log the builder
// tier at startup: candidates, policy, the server's gates and a clock.
// Gates is the serve root's database (P3b plan §4.5), so a tier check that
// reads the gate record sees the server-wide one. The open is best-effort: a
// failure leaves Gates nil -- an empty record -- and the tier log never reads
// it anyway.
func serveTierRuntime(candidates *candidate.Set, pol policy.Policy, root string) relevo.Runtime {
	var gates db.KV
	if d, err := store.New(root).DB(); err == nil {
		gates = d
	}
	return relevo.Runtime{
		Candidates: candidates,
		Policy:     pol,
		Gates:      gates,
		GatesDir:   root,
		Now:        time.Now,
	}
}

// scopeFromPolicy is the systemd scope template a served round launches
// under, from an already-resolved policy scope block (#244, #216, #295).
// nil turns scopes off: sc.Enabled explicitly false. Otherwise (no block,
// or one present but silent on Enabled) scopes are on by default, with
// CPUWeight defaulting to 100 and every other field passed through as
// given (its own zero value means "omit" to ScopeArgv).
func scopeFromPolicy(sc *policy.ScopePolicy) *relevo.ScopeSpec {
	if sc != nil && sc.Enabled != nil && !*sc.Enabled {
		return nil
	}
	spec := &relevo.ScopeSpec{CPUWeight: 100}
	if sc != nil {
		spec.Slice = sc.Slice
		if sc.CPUWeight != 0 {
			spec.CPUWeight = sc.CPUWeight
		}
		spec.MemoryMax = sc.MemoryMax
		spec.CPUQuota = sc.CPUQuota
		spec.GateCPUQuota = sc.GateCPUQuota
		spec.AllowedCPUs = sc.AllowedCPUs
		spec.TasksMax = sc.TasksMax
	}
	return spec
}

// scopeStatusText is the startup line's scopes word for a resolved spec:
// "on (slice relevo.slice, 200%)", "on (200%)", "on (slice relevo.slice)",
// "on", or "off" for nil (#285, #295). A spec carrying a gate quota (#313)
// gains ", gate <quota>" inside the parentheses: "on (slice relevo.slice,
// 200%, gate 300%)". A spec carrying an allowed_cpus pool (#314) gains
// ", cpus <pool>, one per round": "on (cpus 0-2, one per round)".
func scopeStatusText(sc *relevo.ScopeSpec) string {
	if sc == nil {
		return "off"
	}
	var parts []string
	if sc.Slice != "" {
		parts = append(parts, "slice "+sc.Slice)
	}
	if sc.CPUQuota != "" {
		parts = append(parts, sc.CPUQuota)
	}
	if sc.GateCPUQuota != "" {
		parts = append(parts, "gate "+sc.GateCPUQuota)
	}
	if sc.AllowedCPUs != "" {
		parts = append(parts, "cpus "+sc.AllowedCPUs+", one per round")
	}
	if len(parts) == 0 {
		return "on"
	}
	return "on (" + strings.Join(parts, ", ") + ")"
}

func serveAdminConfig(root string) serve.Config {
	return serve.Config{
		Root:   root,
		Runner: proc.New(),
		Now:    time.Now,
	}
}

// loadServeConfig opens the machine database exactly as newRuntime does
// (store.DefaultRoot(), not --state), imports any config file present, and
// returns the loaded config: the set every candidate-aware command needs. It
// is the one copy of that loading: cmdServeRun starts a daemon with it, and
// the gate verbs require it (§4.8).
func loadServeConfig() (config.Loaded, error) {
	root, err := store.DefaultRoot()
	if err != nil {
		return config.Loaded{}, err
	}
	d, err := openDB(filepath.Join(root, "relevo.db"))
	if err != nil {
		return config.Loaded{}, err
	}
	cs := config.Open(d)

	configDir, err := userConfigRoot()
	if err != nil {
		return config.Loaded{}, err
	}
	if !d.Newer() {
		if _, err := cs.ImportFiles(filepath.Join(configDir, "relevo"), time.Now().UTC()); err != nil {
			return config.Loaded{}, err
		}
	} else {
		slog.Warn("relevo.db schema is newer; config import skipped")
	}
	return cs.Load()
}

// serveAdminConfigWithCandidates is serveAdminConfig plus the configured
// candidates and policy, which the gate verbs need: `relevo serve gates`
// projects the ledger onto the candidate set, and the two edit verbs refuse a
// token no candidate names (#251).
//
// Unlike cmdServeRun, an empty candidates set is an error here: with no
// candidate set to project onto, a ledger full of gates would render as
// "no gates", which reads as "nothing is gated" -- the same false negative
// this round exists to remove.
func serveAdminConfigWithCandidates(root string) (serve.Config, error) {
	L, err := loadServeConfig()
	if err != nil {
		return serve.Config{}, err
	}
	if L.Candidates.Len() == 0 {
		return serve.Config{}, errors.New("no candidates configured")
	}

	cfg := serveAdminConfig(root)
	cfg.Candidates = L.Candidates
	cfg.Policy = L.Policy
	cfg.Registry = L.Registry
	return cfg, nil
}

func cmdServeRun(args []string) error {
	fs, sf := serveFlagSet()
	fs.SetOutput(os.Stderr)
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}

	root, err := serveRoot(fs)
	if err != nil {
		return err
	}

	L, err := loadServeConfig()
	if err != nil {
		return err
	}
	candidates, pol, reg := L.Candidates, L.Policy, L.Registry

	builderTierRT := serveTierRuntime(candidates, pol, root)
	if builderTier := relevo.ServedBuilderTier(builderTierRT); builderTier == harness.TierHarness {
		slog.Warn(relevo.ServerTierWarning(relevo.ServerProbe{TierAware: true, BuilderTier: string(builderTier)}))
	} else {
		slog.Info("builder tier", "tier", builderTier)
	}

	// Log each candidate harness's role-file coverage once at startup
	// (#238): a candidate whose kind is missing role files is gated on
	// every pick, silently until now.
	roles := harness.OSRoleChecker()
	seenKinds := map[string]bool{}
	for _, ref := range candidates.Refs() {
		r, err := candidate.ParseRef(ref)
		if err != nil || seenKinds[r.Harness] {
			continue
		}
		seenKinds[r.Harness] = true
		// The gate checks the builder's resolved definitions: in roles.json
		// mode a custom name may be what the row launches (#374 §5).
		spec, err := reg.Spec("builder", r.Harness)
		if err != nil {
			continue
		}
		if missing := roles.Missing(r.Harness, spec.Definitions); len(missing) > 0 {
			slog.Warn("candidate roles missing; those candidates will be skipped", "harness", r.Harness, "missing", missing, "fix", "relevo config agents --kind "+r.Harness)
		} else {
			slog.Info("roles present", "harness", r.Harness)
		}
	}

	reader, prices := newUsageReader(L.Prices)

	hooksCfg, err := resolveHooksConfig(L.Hooks, nil)
	if err != nil {
		return err
	}
	dispatcher := newHooksDispatcher(hooksCfg, pol)

	// Scopes are enabled by policy (default on) and confirmed by a startup
	// probe (#244, #216); if systemd-run is missing or the user manager
	// refuses, scopes are off for the daemon's lifetime with one log line.
	scopesStatus := "off"
	scope := scopeFromPolicy(pol.ScopeFor(true))
	if scope != nil {
		if err := proc.ProbeScopes(context.Background(), scope.Slice); err != nil {
			slog.Warn("scopes unavailable; builders will run in the daemon's cgroup", "err", err)
			scope = nil
			scopesStatus = "unavailable"
		} else {
			scopesStatus = scopeStatusText(scope)
		}
	}

	cfg := serve.Config{
		Root:           root,
		Candidates:     candidates,
		Policy:         pol,
		Runner:         proc.New(),
		Git:            git.NewClient("git", 0, 0),
		Now:            time.Now,
		Interval:       sf.interval,
		MaxBundleBytes: sf.maxBundleBytes,
		Usage:          reader,
		Prices:         prices,
		StartedAt:      time.Now(),
		Roles:          roles,
		Registry:       reg,
		MaxBuilders:    sf.maxBuilders,
		Hooks:          dispatcher,
		Scope:          scope,
	}

	srv, err := serve.New(cfg)
	if err != nil {
		return err
	}

	maxBuilders := cfg.MaxBuilders
	if maxBuilders <= 0 {
		maxBuilders = pol.MaxBuildersOrDefault()
	}
	slog.Info(fmt.Sprintf("builders cap=%d scopes=%s", maxBuilders, scopesStatus))

	root, err = filepath.Abs(root)
	if err != nil {
		return err
	}
	def, _ := defaultServeRoot()
	if err := serve.WritePointer(def, serve.DaemonPointer{
		Root:      root,
		PID:       os.Getpid(),
		Listen:    sf.listen,
		StartedAt: time.Now(),
	}); err != nil {
		slog.Warn("daemon pointer not written", "err", err)
	}
	defer func() { _ = serve.RemovePointer(def) }()

	var cert *tls.Certificate
	var fp string
	if !sf.insecureHTTP {
		c, err := serve.LoadTLS(root)
		if err != nil {
			return err
		}
		cert = &c
		fp = serve.FingerprintOf(c.Leaf.Raw)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	go func() {
		for i := 0; i < 100; i++ {
			if addr := srv.Addr(); addr != nil {
				if sf.insecureHTTP {
					fmt.Printf("relevo serve listening on %s (INSECURE http)\n", addr)
				} else {
					fmt.Printf("relevo serve listening on %s (tls %s)\n", addr, fp)
				}
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	return srv.ListenAndServe(ctx, serve.ListenConfig{
		Addr:         sf.listen,
		TLS:          cert,
		InsecureHTTP: sf.insecureHTTP,
	})
}

func cmdServeInit(args []string) error {
	fs := flag.NewFlagSet("relevo serve init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var hosts hostSlice
	fs.Var(&hosts, "host", "hostname or IP to include in certificate SANs")
	_ = fs.String("state", "", "state directory")
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}

	root, err := serveRoot(fs)
	if err != nil {
		return err
	}

	fp, err := serve.InitTLS(root, hosts, time.Now())
	if errors.Is(err, serve.ErrTLSExists) {
		crtPath := filepath.Join(root, "server.crt")
		existingFP, fpErr := serve.Fingerprint(crtPath)
		if fpErr != nil {
			return fpErr
		}
		fmt.Printf("already initialised; fingerprint %s\n", existingFP)
		return nil
	}
	if err != nil {
		return err
	}

	fmt.Printf("fingerprint %s\n", fp)
	fmt.Println(`clients: run relevo serve enroll --label <who> --key "<their relevo config server key line>"`)
	return nil
}

func cmdServeEnroll(args []string) error {
	fs := flag.NewFlagSet("relevo serve enroll", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	label := fs.String("label", "", "client label")
	key := fs.String("key", "", "client ed25519 public key line")
	_ = fs.String("state", "", "state directory")
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}

	if *label == "" || *key == "" {
		fmt.Fprintln(os.Stderr, "usage: relevo serve enroll --label <label> --key \"<ed25519 line>\" [--state <dir>]")
		return exitCodeErr{code: 2}
	}

	root, err := serveRoot(fs)
	if err != nil {
		return err
	}

	clientsPath := filepath.Join(root, "clients.json")
	clients, err := serve.LoadClients(clientsPath)
	if err != nil {
		return err
	}

	cl, err := clients.Add(*label, *key, time.Now())
	if errors.Is(err, serve.ErrAlreadyEnrolled) {
		fmt.Fprintf(os.Stderr, "relevo serve enroll: client already enrolled: %s\n", *label)
		return exitCodeErr{code: 1}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "relevo serve enroll: %v\n", err)
		return exitCodeErr{code: 1}
	}

	fmt.Printf("enrolled %s %s\n", cl.Label, cl.ID)
	return nil
}

func cmdServeClients(args []string) error {
	fs := flag.NewFlagSet("relevo serve clients", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	_ = fs.String("state", "", "state directory")
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}

	root, err := adminRoot(fs)
	if err != nil {
		return err
	}

	clientsPath := filepath.Join(root, "clients.json")
	clients, err := serve.LoadClients(clientsPath)
	if err != nil {
		return err
	}

	fmt.Print(serve.RenderClients(clients.List()))
	return nil
}

func cmdServeRevoke(args []string) error {
	fs := flag.NewFlagSet("relevo serve revoke", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	_ = fs.String("state", "", "state directory")
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: relevo serve revoke <id> [--state <dir>]")
		return exitCodeErr{code: 2}
	}

	id := fs.Arg(0)
	root, err := serveRoot(fs)
	if err != nil {
		return err
	}

	clientsPath := filepath.Join(root, "clients.json")
	clients, err := serve.LoadClients(clientsPath)
	if err != nil {
		return err
	}

	if err := clients.Revoke(remote.ClientID(id), time.Now()); err != nil {
		if errors.Is(err, serve.ErrNoSuchClient) {
			fmt.Fprintf(os.Stderr, "relevo serve revoke: no such client %s\n", id)
			return exitCodeErr{code: 1}
		}
		return err
	}
	return nil
}

func cmdServeFingerprint(args []string) error {
	fs := flag.NewFlagSet("relevo serve fingerprint", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	_ = fs.String("state", "", "state directory")
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}

	root, err := adminRoot(fs)
	if err != nil {
		return err
	}

	crtPath := filepath.Join(root, "server.crt")
	fp, err := serve.Fingerprint(crtPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "relevo serve fingerprint: %v\n", err)
		return exitCodeErr{code: 1}
	}

	fmt.Println(fp)
	return nil
}

func cmdServeStatus(args []string) error {
	fs := flag.NewFlagSet("relevo serve status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	_ = fs.String("state", "", "state directory")
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}

	root, err := adminRoot(fs)
	if err != nil {
		return err
	}

	srv, err := serve.New(serveAdminConfig(root))
	if err != nil {
		return err
	}

	owners, builders, err := serve.AdminStatus(context.Background(), srv)
	if err != nil {
		return err
	}

	fmt.Print(serve.RenderAdminStatus(owners, builders))
	return nil
}

// cmdServeLog prints one owner's binding log from the server (#216): the
// read-only counterpart of `relevo log`. It resolves the owner with the same
// resolver `serve unbind` uses, builds that owner's runtime, and renders
// through the same printLog the client verb uses, so the output reads exactly
// like a client's. It stamps nothing -- the .viewed sidecar is the owner's,
// not the admin's -- and creates nothing.
func cmdServeLog(args []string) error {
	const usage = "usage: relevo serve log --owner <label|id> <name> [--round N] [--after N] [--json] [--follow] [--state <dir>]"

	fs := flag.NewFlagSet("relevo serve log", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprintln(fs.Output(), usage) }
	owner := fs.String("owner", "", "client label or id")
	round := fs.Int("round", 0, "show only this round (default: all rounds)")
	after := fs.Int("after", 0, "show only entries with a Seq greater than this (0 = all)")
	asJSON := fs.Bool("json", false, "one compact JSON object per line (NDJSON)")
	follow := fs.Bool("follow", false, "keep printing new entries until the binding is DONE or removed")
	_ = fs.String("state", "", "state directory")
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}

	if *owner == "" || fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, usage)
		return exitCodeErr{code: 2}
	}
	name := fs.Arg(0)

	root, err := adminRoot(fs)
	if err != nil {
		return err
	}

	srv, err := serve.New(serveAdminConfig(root))
	if err != nil {
		return err
	}

	rt, _, err := serve.AdminOwnerRuntime(srv, *owner)
	if err != nil {
		if errors.Is(err, serve.ErrNoSuchClient) {
			fmt.Fprintf(os.Stderr, "relevo serve log: no such client: %s\n", *owner)
		} else if errors.Is(err, store.ErrNotFound) {
			fmt.Fprintf(os.Stderr, "relevo serve log: %s/%s: binding not found\n", *owner, name)
		} else {
			fmt.Fprintf(os.Stderr, "relevo serve log: %v\n", err)
		}
		return exitCodeErr{code: 1}
	}

	if err := printLog(rt, name, *round, *after, *asJSON, *follow, false); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fmt.Fprintf(os.Stderr, "relevo serve log: %s/%s: binding not found\n", *owner, name)
		} else {
			fmt.Fprintf(os.Stderr, "relevo serve log: %v\n", err)
		}
		return exitCodeErr{code: 1}
	}
	return nil
}

// cmdServeShow prints one owner's round from the server (#216): the read-only
// counterpart of `relevo show`. It reads live bindings only -- opening the
// database would create it, and the database belongs to the client that ran
// the work, not to the server admin's read -- and prefixes the stderr header
// with the owner's label so the reader can see whose round it is.
func cmdServeShow(args []string) error {
	const usage = "usage: relevo serve show --owner <label|id> <name> [--round N] [--plan|--report|--diff|--drift|--log|--transcript] [--json] [--state <dir>]"

	fs := flag.NewFlagSet("relevo serve show", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprintln(fs.Output(), usage) }
	owner := fs.String("owner", "", "client label or id")
	round := fs.Int("round", 0, "the round to read; 0 = the newest completed round")
	plan := fs.Bool("plan", false, "show the plan (default)")
	report := fs.Bool("report", false, "show the report")
	diff := fs.Bool("diff", false, "show the round's captured diff")
	drift := fs.Bool("drift", false, "show the round's drift patch")
	logSection := fs.Bool("log", false, "show the round's log entries")
	transcript := fs.Bool("transcript", false, "show the round's builder transcript")
	asJSON := fs.Bool("json", false, "machine-readable output: the ShowResult, Events included for --log")
	_ = fs.String("state", "", "state directory")
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}

	if *owner == "" || fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, usage)
		return exitCodeErr{code: 2}
	}
	name := fs.Arg(0)

	section, serr := showSectionFlags(*plan, *report, *diff, *drift, *logSection, *transcript, false, "")
	if serr != nil {
		fmt.Fprintln(os.Stderr, "relevo serve show: "+serr.Error())
		fmt.Fprintln(os.Stderr, usage)
		return exitCodeErr{code: 2}
	}

	root, err := adminRoot(fs)
	if err != nil {
		return err
	}

	srv, err := serve.New(serveAdminConfig(root))
	if err != nil {
		return err
	}

	rt, label, err := serve.AdminOwnerRuntime(srv, *owner)
	if err != nil {
		if errors.Is(err, serve.ErrNoSuchClient) {
			fmt.Fprintf(os.Stderr, "relevo serve show: no such client: %s\n", *owner)
		} else if errors.Is(err, store.ErrNotFound) {
			fmt.Fprintf(os.Stderr, "relevo serve show: %s/%s: binding not found (serve show reads live bindings only)\n", *owner, name)
		} else {
			fmt.Fprintf(os.Stderr, "relevo serve show: %v\n", err)
		}
		return exitCodeErr{code: 1}
	}

	opts := relevo.ShowOptions{Name: name, Round: *round, Section: section, JSON: *asJSON}
	if err := printShow(rt, opts, false, false, label+"/"); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fmt.Fprintf(os.Stderr, "relevo serve show: %s/%s: binding not found (serve show reads live bindings only)\n", *owner, name)
		} else {
			fmt.Fprintf(os.Stderr, "relevo serve show: %v\n", err)
		}
		return exitCodeErr{code: 1}
	}
	return nil
}

// cmdServeTab sums recorded usage across owners from the server (#216). With
// --owner it sums that owner's bindings; without it sums every owner, and the
// binding group is "<label>/<name>" while --by owner groups by label. It is
// the spec's "`relevo tab --by owner` on the server", scoped to this verb.
func cmdServeTab(args []string) error {
	const usage = "usage: relevo serve tab [--owner <label|id>] [--since 7d] [--by binding|model|provider|owner] [--json] [--state <dir>]"

	fs := flag.NewFlagSet("relevo serve tab", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprintln(fs.Output(), usage) }
	owner := fs.String("owner", "", "client label or id (default: every owner)")
	since := fs.String("since", "", "only rounds closed after this: 24h, 7d, or YYYY-MM-DD (default: all)")
	by := fs.String("by", "binding", "group rows by binding, model, provider or owner")
	asJSON := fs.Bool("json", false, "machine-readable output")
	_ = fs.String("state", "", "state directory")
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}

	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, usage)
		return exitCodeErr{code: 2}
	}

	cut, err := relevo.ParseSince(*since, time.Now().UTC())
	if err != nil {
		return err
	}

	root, err := adminRoot(fs)
	if err != nil {
		return err
	}

	srv, err := serve.New(serveAdminConfig(root))
	if err != nil {
		return err
	}

	entries, err := serve.AdminTabEntries(srv, *owner, cut, func(msg string) {
		fmt.Fprintf(os.Stderr, "relevo serve tab: skip %s\n", msg)
	})
	if err != nil {
		if errors.Is(err, serve.ErrNoSuchClient) {
			fmt.Fprintf(os.Stderr, "relevo serve tab: no such client: %s\n", *owner)
		} else if errors.Is(err, store.ErrNotFound) {
			fmt.Fprintf(os.Stderr, "relevo serve tab: %s: binding not found\n", *owner)
		} else {
			fmt.Fprintf(os.Stderr, "relevo serve tab: %v\n", err)
		}
		return exitCodeErr{code: 1}
	}

	return renderTabReport(entries, *by, cut, *asJSON)
}

// serveGateList lists the gates on the server-wide ledger: `relevo serve
// gates` was the answer to `relevo gate` printing "nothing was gating" on a
// box whose gates live on the serve root's ledger, not the caller's (§4.3).
func serveGateList(fs *flag.FlagSet) error {
	root, err := adminRoot(fs)
	if err != nil {
		return err
	}

	cfg, err := serveAdminConfigWithCandidates(root)
	if err != nil {
		return fmt.Errorf("relevo gate --serve: %w", err)
	}
	srv, err := serve.New(cfg)
	if err != nil {
		return err
	}

	fmt.Print(serve.RenderGates(serve.AdminGates(srv), time.Now()))
	return nil
}

// serveGateClear lifts the server-side gate on a provider, in place, with
// no forwarding: the verb for the box that runs the daemon (§4.3).
func serveGateClear(fs *flag.FlagSet, subject string) error {
	root, err := adminRoot(fs)
	if err != nil {
		return err
	}

	cfg, err := serveAdminConfigWithCandidates(root)
	if err != nil {
		return fmt.Errorf("relevo gate --serve: %w", err)
	}
	srv, err := serve.New(cfg)
	if err != nil {
		return err
	}

	provider, removed, err := serve.AdminAvailable(srv, subject)
	if err != nil {
		return err
	}

	if removed == 0 {
		fmt.Printf("nothing was gating %s\n", provider)
		return nil
	}
	fmt.Printf("cleared %s (%d entries)\n", provider, removed)
	return nil
}

// serveGateUnavailable records a server-side gate: the server ledger's
// counterpart to gateUnavailable, with no daemon-switch line and no forwarding
// (the gate is already on the ledger the daemon reads) (§4.3).
func serveGateUnavailable(fs *flag.FlagSet, token, forFlag, reason string) error {
	root, err := adminRoot(fs)
	if err != nil {
		return err
	}

	cfg, err := serveAdminConfigWithCandidates(root)
	if err != nil {
		return fmt.Errorf("relevo gate --serve: %w", err)
	}
	srv, err := serve.New(cfg)
	if err != nil {
		return err
	}

	until, err := parseFor(forFlag, time.Now())
	if err != nil {
		return err
	}

	provider, err := serve.AdminUnavailable(srv, token, until, reason)
	if err != nil {
		return err
	}

	count := 0
	for _, ref := range cfg.Candidates.Refs() {
		parsed, err := candidate.ParseRef(ref)
		if err != nil {
			continue
		}
		if parsed.Provider == provider {
			count++
		}
	}

	fmt.Printf("gated %s (%d candidates) %s\n", provider, count, relevo.GateUntilText(until))
	return nil
}

func cmdServeUI(args []string) error {
	fs := flag.NewFlagSet("relevo serve ui", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	_ = fs.String("state", "", "state directory")
	interval := fs.Duration("interval", 0, "poll interval (0 uses the ui default)")
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}

	// The root resolves before any tty check, so an uninitialised --state
	// dir reads as a state error and not a terminal one.
	root, err := adminRoot(fs)
	if err != nil {
		return err
	}

	srv, err := serve.New(serveAdminConfig(root))
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	return ui.RunSource(ctx, ui.ServerSource(srv), ui.Options{
		Interval: *interval,
		Prefs: ui.PrefsStore{
			KV:         srv.DB(),
			Key:        "serve.ui",
			LegacyPath: filepath.Join(root, "ui.json"),
		},
		PipeHint: "relevo serve ui needs a terminal; use relevo serve status when piping",
	})
}

func cmdServeUnbind(args []string) error {
	fs := flag.NewFlagSet("relevo serve unbind", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	owner := fs.String("owner", "", "client label or id")
	force := fs.Bool("force", false, "unbind even if the round is running")
	_ = fs.String("state", "", "state directory")
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}

	if *owner == "" || fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: relevo serve unbind --owner <label|id> <name> [--state <dir>] [--force]")
		return exitCodeErr{code: 2}
	}

	name := fs.Arg(0)
	root, err := adminRoot(fs)
	if err != nil {
		return err
	}

	srv, err := serve.New(serveAdminConfig(root))
	if err != nil {
		return err
	}

	res, err := serve.AdminUnbind(context.Background(), srv, *owner, name, *force)
	if err != nil {
		fmt.Fprintf(os.Stderr, "relevo serve unbind: %v\n", err)
		return exitCodeErr{code: 1}
	}

	fmt.Printf("unbound %s/%s\n", *owner, name)
	if txt := relevo.UnbindText(name, res); txt != "" {
		fmt.Println(txt)
	}
	return nil
}

func cmdServeGC(args []string) error {
	fs := flag.NewFlagSet("relevo serve gc", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	abandoned := fs.String("abandoned", "", "abandoned duration threshold")
	dryRun := fs.Bool("dry-run", false, "dry run without unbinding")
	_ = fs.String("state", "", "state directory")
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}

	if *abandoned == "" {
		fmt.Fprintln(os.Stderr, "relevo serve gc: --abandoned <duration> is required")
		return exitCodeErr{code: 2}
	}

	dur, err := time.ParseDuration(*abandoned)
	if err != nil {
		fmt.Fprintf(os.Stderr, "relevo serve gc: invalid duration %q: %v\n", *abandoned, err)
		return exitCodeErr{code: 2}
	}

	root, err := adminRoot(fs)
	if err != nil {
		return err
	}

	srv, err := serve.New(serveAdminConfig(root))
	if err != nil {
		return err
	}

	results, err := serve.GCAbandoned(context.Background(), srv, dur, time.Now(), *dryRun)
	if err != nil {
		return err
	}

	for _, r := range results {
		if *dryRun {
			fmt.Printf("%s  %s  abandoned (last seen %s)\n", r.Label, r.Name, r.LastSeen.Format("2006-01-02 15:04:05"))
		} else {
			fmt.Printf("%s  %s  archived to %s\n", r.Label, r.Name, r.Archive)
		}
	}
	return nil
}
