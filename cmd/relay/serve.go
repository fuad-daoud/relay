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
	root, err := store.DefaultRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "serve"), nil
}

func cmdServe(args []string) error {
	const usage = `usage: relay serve [--listen :7777] [--state <dir>] [--interval 2s] [--insecure-http] [--max-bundle-bytes N]
       relay serve init [--host <name>]... [--state <dir>]
       relay serve enroll --label <label> --key "<ed25519 line>" [--state <dir>]
       relay serve clients [--state <dir>]
       relay serve revoke <id> [--state <dir>]
       relay serve fingerprint [--state <dir>]
       relay serve status [--state <dir>]
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
	candidates, err := candidate.Load(filepath.Join(configDir, "relay", "candidates.json"))
	if err != nil {
		return err
	}
	pol, err := policy.Load(filepath.Join(configDir, "relay", "policy.json"))
	if err != nil {
		return err
	}

	builderTierRT := relay.Runtime{Candidates: candidates, Policy: pol, LedgerPath: filepath.Join(root, "ledger.json")}
	if builderTier := relay.ServedBuilderTier(builderTierRT); builderTier == harness.TierHarness {
		slog.Warn(relay.ServerTierWarning(relay.ServerProbe{TierAware: true, BuilderTier: string(builderTier)}))
	} else {
		slog.Info("builder tier", "tier", builderTier)
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
	}

	srv, err := serve.New(cfg)
	if err != nil {
		return err
	}

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

	root, err := serveRoot(fs)
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

	root, err := serveRoot(fs)
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

	root, err := serveRoot(fs)
	if err != nil {
		return err
	}

	srv, err := serve.New(serve.Config{
		Root: root,
		Now:  time.Now,
	})
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
	root, err := serveRoot(fs)
	if err != nil {
		return err
	}

	srv, err := serve.New(serve.Config{
		Root: root,
		Now:  time.Now,
	})
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

	root, err := serveRoot(fs)
	if err != nil {
		return err
	}

	srv, err := serve.New(serve.Config{
		Root: root,
		Now:  time.Now,
	})
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
