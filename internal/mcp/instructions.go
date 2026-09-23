package mcp

// InstructionsChannel is the model-facing text handed back in initialize's
// result when relevo mcp runs in channel mode (spec
// docs/specs/2026-09-21-planner-channel-design.md §3.7, #303 §4.5): events
// arrive as <channel source="relevo"> blocks, and the model acts on each.
const InstructionsChannel = `relevo is handing you round events over this channel instead of
typing them into your input box. A <channel source="relevo" ...> block can
arrive at any time, including mid-turn; when it does, act on it -- it is not
a distraction from your current task, it is the next step of it.

Event kinds, from the block's kind attribute:

  - kind="report": a builder's round closed. The block's body is the
    report, prefixed with which binding and round it is from. A very large
    report is cut short, and the block ends with a line naming the full
    path when it is. Run the project's check command and compare the diff
    against the plan before calling done -- do not call done on the
    report's arrival alone.
  - kind="state" state="needs_you": a binding is stalled on a human
    decision (a repeated failure, an ambiguous plan). Read the body's
    reason, then run relevo status --name <binding> and decide: unavailable,
    or stop.

An event for a binding you did not personally send still belongs to you --
every binding on this planner shares this one channel. Do not ignore an event
because you do not recognize the binding name; run relevo status to catch up.

Three verbs are tools here, callable directly instead of through the shell:

  - status(name?, all?): one binding, or every binding on this planner, or
    (all: true) every binding relevo knows about.
  - send(name, file, tier?, verify?, regate?, dry_run?): hand a binding's
    builder a new round.
  - done(name): mark a binding done once its round is verified.

Every other relevo verb -- bind, add, fork, diff, log, pull, unavailable,
available, policy, and the rest -- is not a tool here; run it with Bash.
`

// InstructionsTools is initialize's text when relevo mcp runs in tools mode
// (#303 §4.5, D6): nothing is pushed, so the model gets each report by
// running the background wait after every send and reading what relevo pull
// prints when that wait exits.
const InstructionsTools = `relevo is running in tools mode: no events arrive on their own. Everything
relevo tells you arrives as the output of a command you started.

After every send, start the background wait for that binding and end your
turn. The send tool's result carries the exact command; it looks like this:

  background wait (run with run_in_background, then end your turn):
    relevo wait --name <binding> --timeout <budget>; relevo pull --name <binding>

Run that with the Bash tool's run_in_background, then end your turn. Claude
Code re-invokes you when the command exits, with its output in the new turn.
The relevo pull half prints the report text -- the same payload a channel
event would have carried -- and marks the entry delivered.

Act on the pull output after every wait exit except WaitTimeout: an unmarked
or halted round still has its report on disk, and relevo pull prints it.

  - Report text: a builder's round closed. Run the project's check command
    and compare the diff against the plan before calling done -- do not call
    done on the report's arrival alone.
  - A needs-you outcome: relevo wait's own line gives the reason; run
    relevo status --name <binding> and decide: send the next round,
    unavailable, or stop.
  - WaitTimeout (the round is still running): run
    relevo status --name <binding>, and start the background wait again if
    the round is still open.
  - Exit without output: relevo pull printing "nothing pending" means another
    route already delivered the report; nothing is owed.

A report or a needs_you for a binding you did not personally send still
belongs to you -- every binding on this planner is yours. Do not ignore a
payload because you do not recognize the binding name; run relevo status to
catch up.

Three verbs are tools here, callable directly instead of through the shell:

  - status(name?, all?): one binding, or every binding on this planner, or
    (all: true) every binding relevo knows about.
  - send(name, file, tier?, verify?, regate?, dry_run?): hand a binding's
    builder a new round.
  - done(name): mark a binding done once its round is verified.

Every other relevo verb -- bind, add, fork, diff, log, pull, wait, unavailable,
available, policy, and the rest -- is not a tool here; run it with Bash.
`

// InstructionsFor picks the text for mode. The mode is known before
// initialize is answered, so the model is told from its first turn which one
// it is in (#303 §4.5).
func InstructionsFor(mode Mode) string {
	if mode == ModeChannel {
		return InstructionsChannel
	}
	return InstructionsTools
}
