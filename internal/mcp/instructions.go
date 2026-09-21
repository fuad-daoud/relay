package mcp

// Instructions is the model-facing text handed back in initialize's result
// (spec docs/specs/2026-09-21-planner-channel-design.md §3.7). It explains
// what a <channel source="relay"> event is and what to do about it, and
// names the four tools; every other relay verb stays on the shell.
const Instructions = `relay is handing you round events over this channel instead of
typing them into your input box. A <channel source="relay" ...> block can
arrive at any time, including mid-turn; when it does, act on it -- it is not
a distraction from your current task, it is the next step of it.

Event kinds, from the block's kind attribute:

  - kind="report": a builder's round closed. The block's body is the
    report, prefixed with which binding and round it is from. Run the
    project's check command and compare the diff against the plan before
    calling done -- do not call done on the report's arrival alone.
  - kind="state" state="needs_you": a binding is stalled on a human
    decision (a blocked dialog, a repeated failure, an ambiguous plan).
    Read the body's reason, then run relay status --name <binding> and
    decide: answer, unavailable, or stop.
  - kind="state" state="broken": a binding's builder pane is gone.
  - kind="state" state="orphaned": relay's records disagree with what pane
    you are running in; investigate before trusting either side.

An event for a binding you did not personally send still belongs to you --
every binding on this pane shares this one channel. Do not ignore an event
because you do not recognize the binding name; run relay status to catch up.

Four verbs are tools here, callable directly instead of through the shell:

  - status(name?, all?): one binding, or every binding on this pane, or
    (all: true) every binding relay knows about.
  - send(name, file, tier?, verify?, regate?, dry_run?): hand a binding's
    builder a new round.
  - answer(name, text? | keys? | choice?): answer a builder blocked at a
    dialog; give exactly one of the three.
  - done(name): mark a binding done once its round is verified.

Every other relay verb -- bind, add, fork, diff, log, pull, unavailable,
available, policy, and the rest -- is not a tool here; run it with Bash.
`
