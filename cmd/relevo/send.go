package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/fuad-daoud/relevo/internal/relevo"
)

func cmdSend(args []string) error {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	file := fs.String("file", "", "path to the plan file to hand the builder")
	name := fs.String("name", "", "binding name (default: the binding for this cwd)")
	tier := fs.String("tier", "", "permission tier: harness|read|edit|yolo (default: candidate tier, then policy tier.<role>, then harness)")
	builder := fs.String("builder", "", "candidate name or harness/provider/model token to run this round and later ones on; refused while a round is open")
	allowYolo := fs.Bool("allow-yolo", false, "permit --tier yolo above policy max_tier for this command")
	dryRun := fs.Bool("dry-run", false, "check every precondition and print what send would do, without sending")
	regate := fs.Int("regate", -1, "after a failing gate, open up to N automatic repair rounds; 0 disables (default: config policy gate.regate)")
	verify := fs.Bool("verify", false, "run a read-only reviewer in a throwaway worktree when the round closes")
	noVerify := fs.Bool("no-verify", false, "do not run a reviewer when the round closes (default: config policy verify.default)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	// Before newRuntime, like add's flag pair: the refusal must not depend on
	// argv order and must touch neither the state directory nor a harness.
	if *verify && *noVerify {
		fmt.Fprintf(os.Stderr, "relevo: relevo send --verify and --no-verify are exclusive\n")
		return fmt.Errorf("relevo send --verify and --no-verify are exclusive: %w", exitCodeErr{code: 2})
	}
	if *file == "" {
		return fmt.Errorf("relevo send requires --file")
	}
	regateOpt, err := regateFlag(fs, regate)
	if err != nil {
		return err
	}

	var verifyOpt *bool
	if *verify || *noVerify {
		v := *verify
		verifyOpt = &v
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	target, err := resolveBinding(rt, *name, fs.Args())
	if err != nil {
		return err
	}

	opts := relevo.SendOptions{
		Tier:      *tier,
		AllowYolo: *allowYolo,
		Builder:   *builder,
		Regate:    regateOpt,
		Verify:    verifyOpt,
	}

	if *dryRun {
		d, err := relevo.SendDryRun(context.Background(), rt, target, *file, opts)
		if err != nil {
			return err
		}
		fmt.Print(relevo.RenderDryRun(d))
		return nil
	}

	res, err := relevo.Send(context.Background(), rt, target, *file, opts)
	if err != nil {
		return err
	}

	if res.Pick != "" {
		fmt.Println(res.Pick)
	}
	if res.Drift != "" {
		fmt.Println(res.Drift)
	}
	fmt.Printf("sent round %d to %s's builder\n", res.Round, target)
	warnWaitingOnYou(rt, target)
	return nil
}
