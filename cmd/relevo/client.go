package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
)

// cmdClient dispatches `relevo client init|add-server|rm-server` (§4.7): this
// machine's remote-builder identity (an ed25519 keypair) and its server
// list (~/.config/relevo/servers.json).
func cmdClient(args []string) error {
	const usage = `usage: relevo client init
       relevo client add-server <name> <url> (--fingerprint sha256:<hex> | --ca system | --insecure)
       relevo client rm-server <name>`

	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return exitCodeErr{code: 2}
	}

	switch args[0] {
	case "init":
		return cmdClientInit(args[1:])
	case "add-server":
		return cmdClientAddServer(args[1:])
	case "rm-server":
		return cmdClientRmServer(args[1:])
	case "help", "-h", "--help":
		fmt.Println(usage)
		return nil
	default:
		fmt.Fprintln(os.Stderr, usage)
		return exitCodeErr{code: 2}
	}
}

// cmdClientInit generates this machine's client key (refusing to overwrite
// one that exists) and prints the id and the enrollment line a server admin
// runs `relevo serve enroll --key "<line>"` with.
func cmdClientInit(args []string) error {
	fs := flag.NewFlagSet("relevo client init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	if _, ok, err := rt.Config.Secret(config.SecretClientKey); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("client key already exists; remove it first to replace it: %w", client.ErrKeyExists)
	}

	kp, err := remote.Generate()
	if err != nil {
		return err
	}
	pem, err := remote.MarshalPrivate(kp)
	if err != nil {
		return err
	}
	if err := rt.Config.PutSecret(config.SecretClientKey, pem); err != nil {
		return err
	}

	fmt.Printf("client id %s\n", remote.IDOf(kp.Public))
	fmt.Println(client.EnrollLine(kp))
	return nil
}

// cmdClientAddServer records a server entry in servers.json and, when
// --fingerprint pins it, checks enrollment once with WhoAmI (§4.7). A 401
// here is not an error: it means the admin has not enrolled this client's
// key yet, and the reply names the enrollment line to hand them.
func cmdClientAddServer(args []string) error {
	fs := flag.NewFlagSet("relevo client add-server", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fingerprint := fs.String("fingerprint", "", "pin the server's certificate fingerprint (sha256:<hex>)")
	ca := fs.String("ca", "", `trust the system CA pool instead of pinning ("system")`)
	insecure := fs.Bool("insecure", false, "allow plain http (no TLS)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	rest := fs.Args()
	if len(rest) != 2 {
		fmt.Fprintln(os.Stderr, "usage: relevo client add-server <name> <url> (--fingerprint sha256:... | --ca system | --insecure)")
		return exitCodeErr{code: 2}
	}
	name, rawURL := rest[0], rest[1]

	set := 0
	if *fingerprint != "" {
		set++
	}
	if *ca != "" {
		set++
	}
	if *insecure {
		set++
	}
	if set > 1 {
		fmt.Fprintln(os.Stderr, "relevo client add-server: --fingerprint, --ca and --insecure are mutually exclusive")
		return exitCodeErr{code: 2}
	}

	entry := client.ServerEntry{URL: rawURL, Fingerprint: *fingerprint, CA: *ca, Insecure: *insecure}
	if err := client.ValidateEntry(entry); err != nil {
		return err
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	L, err := rt.Config.Load()
	if err != nil {
		return err
	}
	servers := L.Servers
	servers[name] = entry
	body, err := client.EncodeServers(servers)
	if err != nil {
		return err
	}
	if _, err := rt.Config.Put(config.Servers, body); err != nil {
		return err
	}
	fmt.Printf("added server %s (%s)\n", name, rawURL)

	if *fingerprint == "" {
		// Pinned by --ca or plain --insecure: there is no fingerprint to pin
		// the transport to, so there is nothing to enrollment-check yet.
		return nil
	}

	if len(L.ClientKey) == 0 {
		fmt.Println("no client key yet; run relevo client init, then relevo client add-server again to check enrollment")
		return nil
	}
	key, err := remote.ParsePrivate(L.ClientKey)
	if err != nil {
		return err
	}

	c := client.New(servers, key, time.Now)
	who, werr := c.WhoAmI(context.Background(), name)
	if werr != nil {
		var httpErr *client.HTTPError
		if errors.As(werr, &httpErr) && httpErr.Status == 401 {
			fmt.Printf("not enrolled on %s: give the admin: %s\n", name, client.EnrollLine(key))
			return nil
		}
		return fmt.Errorf("%s: %w", name, werr)
	}
	fmt.Printf("enrolled as %s\n", who.Label)
	return nil
}

// cmdClientRmServer refuses while any binding in the store still names the
// server (relevo.ServerInUse is the pure rule this checks; it is tested in
// internal/relevo so this thin wrapper needs no harness or network access to
// test the refusal shape -- see CLAUDE.md's CI rule).
func cmdClientRmServer(args []string) error {
	fs := flag.NewFlagSet("relevo client rm-server", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "usage: relevo client rm-server <name>")
		return exitCodeErr{code: 2}
	}
	name := rest[0]

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	bindings, err := rt.Store.List()
	if err != nil {
		return err
	}
	if inUse := relevo.ServerInUse(bindings, name); len(inUse) > 0 {
		return fmt.Errorf("server %q is used by %s; unbind them first", name, strings.Join(inUse, ", "))
	}

	L, err := rt.Config.Load()
	if err != nil {
		return err
	}
	servers := L.Servers
	if _, ok := servers[name]; !ok {
		return fmt.Errorf("no such server %q", name)
	}
	delete(servers, name)
	body, err := client.EncodeServers(servers)
	if err != nil {
		return err
	}
	if _, err := rt.Config.Put(config.Servers, body); err != nil {
		return err
	}
	fmt.Printf("removed server %s\n", name)
	return nil
}

// cmdServers prints one row per configured server: name, url, and this
// client's enrollment on it (§4.7), via the same relevo.ProbeServers doctor's
// per-server checks use.
func cmdServers(args []string) error {
	fs := flag.NewFlagSet("relevo servers", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	L, err := rt.Config.Load()
	if err != nil {
		return err
	}
	servers := L.Servers
	if len(servers) == 0 {
		fmt.Println("no servers configured; relevo client add-server <name> <url>")
		return nil
	}

	var remoteRT relevo.Runtime
	enrollLine := ""
	if key, kerr := remote.ParsePrivate(L.ClientKey); kerr == nil {
		remoteRT.Remote = client.New(servers, key, time.Now)
		enrollLine = client.EnrollLine(key)
	}

	fmt.Print(relevo.RenderServers(relevo.ProbeServers(context.Background(), remoteRT, servers, enrollLine)))
	return nil
}
