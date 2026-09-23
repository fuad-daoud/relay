package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fuad-daoud/relay/internal/candidate"
	"github.com/fuad-daoud/relay/internal/policy"
	"github.com/fuad-daoud/relay/internal/relay"
	"github.com/fuad-daoud/relay/internal/roles"
)

// cmdRoles prints the roles registry, and translates today's candidates.json
// and policy.json into a roles.json (#374 §3.5). Both commands are thin: their
// logic lives in relay.FormatRoles and roles.FromLegacy.
func cmdRoles(args []string) error {
	const usage = `usage: relay roles
       relay roles init [--dry-run] [--force]`

	if len(args) == 0 {
		rt, err := newRuntime()
		if err != nil {
			return err
		}
		fmt.Print(relay.FormatRoles(rt.RoleRegistry()))
		return nil
	}

	switch args[0] {
	case "init":
		return cmdRolesInit(args[1:])
	case "help", "-h", "--help":
		fmt.Fprintln(os.Stderr, usage)
		return nil
	default:
		fmt.Fprintf(os.Stderr, "relay roles: unknown command %q\n", args[0])
		return exitCodeErr{code: 2}
	}
}

// cmdRolesInit writes <config>/relay/roles.json from the candidates.json and
// policy.json beside it. --dry-run prints the file and the notes instead of
// writing, and an existing roles.json is refused without --force.
func cmdRolesInit(args []string) error {
	fs := flag.NewFlagSet("roles init", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "print roles.json and the notes instead of writing it")
	force := fs.Bool("force", false, "overwrite an existing roles.json")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	configDir, err := userConfigRoot()
	if err != nil {
		return err
	}
	dir := filepath.Join(configDir, "relay")

	cands, err := candidate.Load(filepath.Join(dir, "candidates.json"))
	if err != nil {
		return err
	}
	pol, err := policy.Load(filepath.Join(dir, "policy.json"))
	if err != nil {
		return err
	}

	f, notes, err := roles.FromLegacy(cands, pol)
	if err != nil {
		return err
	}
	data, err := f.Encode()
	if err != nil {
		return err
	}

	if *dryRun {
		os.Stdout.Write(data)
		for _, note := range notes {
			fmt.Fprintln(os.Stderr, "relay: "+note)
		}
		return nil
	}

	path := filepath.Join(dir, "roles.json")
	if _, err := os.Stat(path); err == nil && !*force {
		return fmt.Errorf("%s exists; pass --force to overwrite it", path)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		os.Remove(tmp) // best effort: leave no half-written file behind
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp) // best effort: leave no half-written file behind
		return err
	}

	fmt.Printf("wrote %s\n", path)
	for _, note := range notes {
		fmt.Println(note)
	}
	fmt.Println("relay doctor now lists the candidates.json / policy.json fields roles.json replaces; delete them once every relay process is upgraded")
	return nil
}
