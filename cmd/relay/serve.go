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

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/git"
	"github.com/fuad-daoud/relay/internal/harness"
	"github.com/fuad-daoud/relay/internal/policy"
	"github.com/fuad-daoud/relay/internal/proc"
	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/remote"
	"github.com/fuad-daoud/relay/internal/serve"
	"github.com/fuad-daoud/relay/internal/store"
	"github.com/fuad-daoud/relay/internal/ui"
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
}

func serveFlagSet() (*flag.FlagSet, *serveFlags) {
	fs := flag.NewFlagSet("relay serve", flag.ContinueOnError)
	var sf serveFlags
	fs.StringVar(&sf.listen, "listen", ":7777", "listen address")
	fs.StringVar(&sf.state, "state", "", "state directory (defaults to $XDG_STATE_HOME/relay)")
	fs.DurationVar(&sf.interval, "interval", 2*time.Second, "poll interval")
	fs.BoolVar(&sf.insecureHTTP, "insecure-http", false, "serve plain HTTP without TLS")
	fs.Int64Var(&sf.maxBundleBytes, "max-bundle-bytes", 512<<20, "maximum bundle size in bytes")
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
		fmt.Fprintf(os.Stderr, "relay serve: %s\n", note)
	}
	initialised, err := serve.Initialised(root)
	if err != nil {
		return "", err
	}
	if !initialised {
		return "", fmt.Errorf("no serve state at %s: run relay serve init, or pass --state <dir> matching the daemon's", root)
	}
	return root, nil
}

func cmdServe(args []string) error {
	const usage = `usage: relay serve [--listen :7777] [--state <dir>] [--interval 2s] [--insecure-http] [--max-bundle-bytes N]
       relay serve init [--host <name>]... [--state <dir>]
       relay serve enroll --label <label> --key "<ed25519 line>" [--state <dir>]
       relay serve clients [--state <dir>]
       relay serve revoke <id> [--state <dir>]
       relay serve fingerprint [--state <dir>]
       relay serve status [--state <dir>]
       relay serve gates [--state <dir>]
       relay serve available <provider|token> [--state <dir>]
       relay serve unavailable <token> [--for D] [--reason S] [--state <dir>]
       relay serve ui [--state <dir>] [--interval 2s]
       relay serve gc --abandoned <duration> [--dry-run] [--state <dir>]
       relay serve unbind --owner <label|id> <name> [--state <dir>] [--force]`

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
	case "gates":
		return cmdServeGates(args[1:])
	case "available":
		return cmdServeAvailable(args[1:])
	case "unavailable":
		return cmdServeUnavailable(args[1:])
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
		fmt.Fprintf(os.Stderr, "relay serve: unknown command %q\n", args[0])
		return exitCodeErr{code: 2}
	}
}

// serveTierRuntime is the Runtime cmdServeRun uses only to log the builder
// tier at startup: candidates, policy, the server ledger and a clock.
// It is a pure constructor: no I/O beyond what the caller already loaded.
func serveTierRuntime(candidates *candidate.Set, pol policy.Policy, root string) relay.Runtime {
	return relay.Runtime{
		Candidates: candidates,
		Policy:     pol,
		LedgerPath: filepath.Join(root, "ledger.json"),
		Now:        time.Now,
	}
}

func serveAdminConfig(root string) serve.Config {
	return serve.Config{
		Root:   root,
		Runner: proc.New(),
		Now:    time.Now,
	}
}

// loadCandidatesAndPolicy reads the machine's candidates.json and
// policy.json, the pair every candidate-aware command needs. It is the one
// copy of that loading: cmdServeRun starts a daemon with it, and the gate
// verbs require it.
func loadCandidatesAndPolicy(configDir string) (*candidate.Set, policy.Policy, error) {
	candidates, err := candidate.Load(filepath.Join(configDir, "relay", "candidates.json"))
	if err != nil {
		return nil, policy.Policy{}, err
	}
	pol, err := policy.Load(filepath.Join(configDir, "relay", "policy.json"))
	if err != nil {
		return nil, policy.Policy{}, err
	}
	return candidates, pol, nil
}

// serveAdminConfigWithCandidates is serveAdminConfig plus the configured
// candidates and policy, which the gate verbs need: `relay serve gates`
// projects the ledger onto the candidate set, and the two edit verbs refuse a
// token no candidate names (#251).
//
// Unlike cmdServeRun, a missing candidates.json is an error here: with no
// candidate set to project onto, a ledger full of gates would render as
// "no gates", which reads as "nothing is gated" -- the same false negative
// this round exists to remove.
func serveAdminConfigWithCandidates(root string) (serve.Config, error) {
	configDir, err := userConfigRoot()
	if err != nil {
		return serve.Config{}, err
	}
	candPath := filepath.Join(configDir, "relay", "candidates.json")
	if _, err := os.Stat(candPath); err != nil {
		return serve.Config{}, fmt.Errorf("no candidates at %s", candPath)
	}
	candidates, pol, err := loadCandidatesAndPolicy(configDir)
	if err != nil {
		return serve.Config{}, err
	}

	cfg := serveAdminConfig(root)
	cfg.Candidates = candidates
	cfg.Policy = pol
	return cfg, nil
}

func cmdServeRun(args []string) error {
	fs, sf := serveFlagSet()
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return exitCodeErr{code: 2}
	}

	root, err := serveRoot(fs)
	if err != nil {
		return err
	}

	configDir, err := userConfigRoot()
	if err != nil {
		return err
	}
	candidates, pol, err := loadCandidatesAndPolicy(configDir)
	if err != nil {
		return err
	}

	builderTierRT := serveTierRuntime(candidates, pol, root)
	if builderTier := relay.ServedBuilderTier(builderTierRT); builderTier == harness.TierHarness {
		slog.Warn(relay.ServerTierWarning(relay.ServerProbe{TierAware: true, BuilderTier: string(builderTier)}))
	} else {
		slog.Info("builder tier", "tier", builderTier)
	}

	reader, prices := newUsageReader(configDir)

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
	}

	srv, err := serve.New(cfg)
	if err != nil {
		return err
	}
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
					fmt.Printf("relay serve listening on %s (INSECURE http)\n", addr)
				} else {
					fmt.Printf("relay serve listening on %s (tls %s)\n", addr, fp)
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
	fs := flag.NewFlagSet("relay serve init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var hosts hostSlice
	fs.Var(&hosts, "host", "hostname or IP to include in certificate SANs")
	_ = fs.String("state", "", "state directory")
	if err := fs.Parse(args); err != nil {
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
	fmt.Println(`clients: run relay serve enroll --label <who> --key "<their relay client init line>"`)
	return nil
}

func cmdServeEnroll(args []string) error {
	fs := flag.NewFlagSet("relay serve enroll", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	label := fs.String("label", "", "client label")
	key := fs.String("key", "", "client ed25519 public key line")
	_ = fs.String("state", "", "state directory")
	if err := fs.Parse(args); err != nil {
		return exitCodeErr{code: 2}
	}

	if *label == "" || *key == "" {
		fmt.Fprintln(os.Stderr, "usage: relay serve enroll --label <label> --key \"<ed25519 line>\" [--state <dir>]")
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
		fmt.Fprintf(os.Stderr, "relay serve enroll: client already enrolled: %s\n", *label)
		return exitCodeErr{code: 1}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "relay serve enroll: %v\n", err)
		return exitCodeErr{code: 1}
	}

	fmt.Printf("enrolled %s %s\n", cl.Label, cl.ID)
	return nil
}

func cmdServeClients(args []string) error {
	fs := flag.NewFlagSet("relay serve clients", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	_ = fs.String("state", "", "state directory")
	if err := fs.Parse(args); err != nil {
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
	fs := flag.NewFlagSet("relay serve revoke", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	_ = fs.String("state", "", "state directory")
	if err := fs.Parse(args); err != nil {
		return exitCodeErr{code: 2}
	}

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: relay serve revoke <id> [--state <dir>]")
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
			fmt.Fprintf(os.Stderr, "relay serve revoke: no such client %s\n", id)
			return exitCodeErr{code: 1}
		}
		return err
	}
	return nil
}

func cmdServeFingerprint(args []string) error {
	fs := flag.NewFlagSet("relay serve fingerprint", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	_ = fs.String("state", "", "state directory")
	if err := fs.Parse(args); err != nil {
		return exitCodeErr{code: 2}
	}

	root, err := adminRoot(fs)
	if err != nil {
		return err
	}

	crtPath := filepath.Join(root, "server.crt")
	fp, err := serve.Fingerprint(crtPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "relay serve fingerprint: %v\n", err)
		return exitCodeErr{code: 1}
	}

	fmt.Println(fp)
	return nil
}

func cmdServeStatus(args []string) error {
	fs := flag.NewFlagSet("relay serve status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	_ = fs.String("state", "", "state directory")
	if err := fs.Parse(args); err != nil {
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

	owners, err := serve.AdminStatus(context.Background(), srv)
	if err != nil {
		return err
	}

	fmt.Print(serve.RenderAdminStatus(owners))
	return nil
}

// cmdServeGates lists the gates on the server-wide ledger: `relay serve
// gates` is the answer to `relay available` printing "nothing was gating"
// on a box whose gates live on the serve root's ledger, not the caller's.
func cmdServeGates(args []string) error {
	fs := flag.NewFlagSet("relay serve gates", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	_ = fs.String("state", "", "state directory")
	if err := fs.Parse(args); err != nil {
		return exitCodeErr{code: 2}
	}

	root, err := adminRoot(fs)
	if err != nil {
		return err
	}

	cfg, err := serveAdminConfigWithCandidates(root)
	if err != nil {
		return fmt.Errorf("relay serve gates: %w", err)
	}
	srv, err := serve.New(cfg)
	if err != nil {
		return err
	}

	fmt.Print(serve.RenderGates(serve.AdminGates(srv), time.Now()))
	return nil
}

// cmdServeAvailable lifts the server-side gate on a provider, in place, with
// no forwarding: this is the verb for the box that runs the daemon.
func cmdServeAvailable(args []string) error {
	fs := flag.NewFlagSet("relay serve available", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	_ = fs.String("state", "", "state directory")
	if err := fs.Parse(args); err != nil {
		return exitCodeErr{code: 2}
	}

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: relay serve available <provider|token> [--state <dir>]")
		return exitCodeErr{code: 2}
	}
	subject := fs.Arg(0)

	root, err := adminRoot(fs)
	if err != nil {
		return err
	}

	cfg, err := serveAdminConfigWithCandidates(root)
	if err != nil {
		return fmt.Errorf("relay serve available: %w", err)
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

// cmdServeUnavailable records a server-side gate: the server ledger's
// counterpart to cmdUnavailable, with no daemon-switch line and no forwarding
// (the gate is already on the ledger the daemon reads).
func cmdServeUnavailable(args []string) error {
	fs := flag.NewFlagSet("relay serve unavailable", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	forFlag := fs.String("for", "", "how long to gate the provider (Go duration, e.g. 2h); omit to leave it gated until `relay serve available`")
	reason := fs.String("reason", "", "why, for the record")
	_ = fs.String("state", "", "state directory")
	if err := fs.Parse(args); err != nil {
		return exitCodeErr{code: 2}
	}

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: relay serve unavailable <token> [--for D] [--reason S] [--state <dir>]")
		return exitCodeErr{code: 2}
	}
	token := fs.Arg(0)

	root, err := adminRoot(fs)
	if err != nil {
		return err
	}

	cfg, err := serveAdminConfigWithCandidates(root)
	if err != nil {
		return fmt.Errorf("relay serve unavailable: %w", err)
	}
	srv, err := serve.New(cfg)
	if err != nil {
		return err
	}

	until, err := parseFor(*forFlag, time.Now())
	if err != nil {
		return err
	}

	provider, err := serve.AdminUnavailable(srv, token, until, *reason)
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

	fmt.Printf("gated %s (%d candidates) %s\n", provider, count, relay.GateUntilText(until))
	return nil
}

func cmdServeUI(args []string) error {
	fs := flag.NewFlagSet("relay serve ui", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	_ = fs.String("state", "", "state directory")
	interval := fs.Duration("interval", 0, "poll interval (0 uses the ui default)")
	if err := fs.Parse(args); err != nil {
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
		Interval:  *interval,
		PrefsPath: filepath.Join(root, "ui.json"),
		PipeHint:  "relay serve ui needs a terminal; use relay serve status when piping",
	})
}

func cmdServeUnbind(args []string) error {
	fs := flag.NewFlagSet("relay serve unbind", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	owner := fs.String("owner", "", "client label or id")
	force := fs.Bool("force", false, "unbind even if the round is running")
	_ = fs.String("state", "", "state directory")
	if err := fs.Parse(args); err != nil {
		return exitCodeErr{code: 2}
	}

	if *owner == "" || fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: relay serve unbind --owner <label|id> <name> [--state <dir>] [--force]")
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
		fmt.Fprintf(os.Stderr, "relay serve unbind: %v\n", err)
		return exitCodeErr{code: 1}
	}

	fmt.Printf("unbound %s/%s\n", *owner, name)
	if txt := relay.UnbindText(name, res); txt != "" {
		fmt.Println(txt)
	}
	return nil
}

func cmdServeGC(args []string) error {
	fs := flag.NewFlagSet("relay serve gc", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	abandoned := fs.String("abandoned", "", "abandoned duration threshold")
	dryRun := fs.Bool("dry-run", false, "dry run without unbinding")
	_ = fs.String("state", "", "state directory")
	if err := fs.Parse(args); err != nil {
		return exitCodeErr{code: 2}
	}

	if *abandoned == "" {
		fmt.Fprintln(os.Stderr, "relay serve gc: --abandoned <duration> is required")
		return exitCodeErr{code: 2}
	}

	dur, err := time.ParseDuration(*abandoned)
	if err != nil {
		fmt.Fprintf(os.Stderr, "relay serve gc: invalid duration %q: %v\n", *abandoned, err)
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
