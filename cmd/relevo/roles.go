package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// cmdRolesInit writes the roles section from the candidates and policy the
// runtime loaded. --dry-run prints the data and the notes instead of writing,
// and an existing roles section is refused without --force. Its logic lives in
// roles.FromLegacy.
func cmdRolesInit(args []string) error {
	fs := flag.NewFlagSet("relevo config roles-init", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "print the roles section and the notes instead of writing it")
	force := fs.Bool("force", false, "overwrite an existing roles section")
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
	fmt.Println("relevo doctor now lists the candidates and policy fields the roles section replaces")
	return nil
}
