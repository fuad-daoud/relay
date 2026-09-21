package main

import (
	"context"
	"flag"
	"fmt"

	"github.com/fuad-daoud/relay/internal/relay"
)

// cmdReview turns a path:line comments file into a follow-up plan quoting
// each anchored comment's hunk from the round's captured diff. It is not
// destructive -- reading and rendering a plan touches nothing the builder
// owns -- so, like diff, it resolves the binding from the CWD when --name
// and a positional are both omitted.
func cmdReview(args []string) error {
	fs := flag.NewFlagSet("review", flag.ContinueOnError)
	name := fs.String("name", "", "binding name (default: the binding for this cwd)")
	file := fs.String("file", "", "comments file: one path:line: text (or a general comment) per line")
	round := fs.Int("round", 0, "round to review (default: newest completed round)")
	out := fs.String("out", "", "where to write the review plan (default: <binding dir>/NNN-review-plan.md)")
	send := fs.Bool("send", false, "hand the review plan to the builder as the next round")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *file == "" {
		return fmt.Errorf("relay review requires --file")
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	target, err := resolveBinding(rt, *name, fs.Args())
	if err != nil {
		return err
	}

	res, err := relay.Review(context.Background(), rt, relay.ReviewOptions{
		Name:  target,
		File:  *file,
		Round: *round,
		Out:   *out,
		Send:  *send,
	})
	if err != nil {
		return err
	}

	fmt.Printf("wrote review plan for round %d of %s: %s (%d comment(s), %d general)\n",
		res.Round, target, res.PlanPath, res.Comments, res.General)
	if res.Sent != nil {
		fmt.Printf("sent round %d to %s's builder\n", res.Sent.Round, target)
	}
	return nil
}
