package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// cmdRoles prints the roles registry, and translates today's candidates.json
// and policy.json into a roles.json (#374 §3.5). Both commands are thin: their
// logic lives in relevo.FormatRoles and roles.FromLegacy.
func cmdRoles(args []string) error {
	const usage = `usage: relevo roles
       relevo roles init [--dry-run] [--force]`

	if len(args) == 0 {
		rt, err := newRuntime()
		if err != nil {
			return err
		}
		fmt.Print(relevo.FormatRoles(rt.RoleRegistry()))
		return nil
	}

	switch args[0] {
	case "init":
		return cmdRolesInit(args[1:])
	case "help", "-h", "--help":
		fmt.Fprintln(os.Stderr, usage)
		return nil
	default:
		fmt.Fprintf(os.Stderr, "relevo roles: unknown command %q\n", args[0])
		return exitCodeErr{code: 2}
	}
}

// cmdRolesInit writes the roles section from the candidates and policy the
// runtime loaded. --dry-run prints the data and the notes instead of writing,
// and an existing roles section is refused without --force.
func cmdRolesInit(args []string) error {
	fs := flag.NewFlagSet("roles init", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "print roles.json and the notes instead of writing it")
	force := fs.Bool("force", false, "overwrite an existing roles.json")
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

	f, notes, err := roles.FromLegacy(L.Candidates, L.Policy)
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
			fmt.Fprintln(os.Stderr, "relevo: "+note)
		}
		return nil
	}

	exists, err := rt.Config.Has(config.Roles)
	if err != nil {
		return err
	}
	if exists && !*force {
		return errors.New("roles section exists; pass --force to overwrite it")
	}
	if _, err := rt.Config.Put(config.Roles, data); err != nil {
		return err
	}

	fmt.Println("wrote roles")
	for _, note := range notes {
		fmt.Println(note)
	}
	fmt.Println("relevo doctor now lists the candidates.json / policy.json fields roles.json replaces; delete them once every relevo process is upgraded")
	return nil
}
