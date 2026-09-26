package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// askFlagValues holds the pointers ask's flags parse into. askFlagSet defines
// them on fs; cmdAsk and TestAskFlagsHaveActorNotRole read the same surface
// (A2 round 3 S1).
type askFlagValues struct {
	actor       *string
	cand        *string
	file        *string
	question    *string
	round       *int
	nameFlag    *string
	plannerFlag *string
}

// askFlagSet defines ask's flags on fs and returns the values they parse into.
func askFlagSet(fs *flag.FlagSet) *askFlagValues {
	v := &askFlagValues{}
	v.actor = fs.String("actor", "", "the reader actor to consult: reviewer, researcher, or a reader actor in config actors")
	v.cand = fs.String("candidate", "", "candidate name or harness/provider/model token; omit to take the first ungated in config policy order[<role>]")
	v.file = fs.String("file", "", "file containing the question")
	v.question = fs.String("question", "", "the question itself; with --round, exactly one of --file and -q")
	fs.StringVar(v.question, "q", "", "the question itself (shorthand for --question)")
	v.round = fs.Int("round", 0, "ask the builder that built this closed round: resumes its session, headless and read-only")
	v.nameFlag = fs.String("name", "", "binding name")
	v.plannerFlag = fs.String("planner", "", "act as this planner (id or name; default: $RELEVO_PLANNER, else this session's host)")
	return v
}

func cmdAsk(args []string) error {
	fs := flag.NewFlagSet("ask", flag.ContinueOnError)
	v := askFlagSet(fs)
	role, cand, file, question, round := v.actor, v.cand, v.file, v.question, v.round
	nameFlag, plannerFlag := v.nameFlag, v.plannerFlag
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *round > 0 {
		// The round's recorded session fixes the actor and the candidate, so
		// neither is required; --actor is only worth a note.
		if *role != "" {
			fmt.Fprintln(os.Stderr, "note: relevo ask --round ignores --actor; the resumed session fixes the actor")
		}
		if (*file != "") == (*question != "") {
			return fmt.Errorf("relevo ask --round needs --file or -q")
		}
	} else {
		if *role == "" {
			return fmt.Errorf("relevo ask needs --actor ACTOR")
		}
		if *file == "" {
			return fmt.Errorf("relevo ask needs --file PATH")
		}
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	name, err := resolveBinding(rt, *nameFlag, fs.Args())
	if err != nil {
		return err
	}

	res, err := relevo.Ask(context.Background(), rt, relevo.AskOptions{
		Role:      *role,
		Candidate: *cand,
		File:      *file,
		Question:  *question,
		Round:     *round,
		Name:      name,
		PlannerID: *plannerFlag,
	})
	if err != nil {
		return err
	}

	if *round > 0 {
		fmt.Printf("asked round %d's builder (%s session %s) on %s (pid %d)\nfindings: %s\n",
			*round, res.Consult.Endpoint.Kind, res.Consult.Endpoint.SessionID, res.Binding,
			res.Consult.Endpoint.PID, delivery.FindingsCommand(res.Binding, res.Consult.Round, res.Consult.ID))
		return nil
	}

	fmt.Printf("asked %s consult %s on %s (pid %d)\nfindings: %s\n",
		res.Consult.Role, res.Consult.ID, res.Binding, res.Consult.Endpoint.PID,
		delivery.FindingsCommand(res.Binding, res.Consult.Round, res.Consult.ID))
	if n := availability.GatedNote(relevo.AvailabilityDeps(rt), res.Candidate); n != "" {
		fmt.Fprintln(os.Stderr, n)
	}
	notePick(rt, *role, res.Resolution)
	return nil
}
