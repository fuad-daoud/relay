package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/fuad-daoud/relay/internal/mcp"
	"github.com/fuad-daoud/relay/internal/relay"
)

// minMCPInterval floors --interval, the same guard the daemon's --interval
// gets, so a misconfigured poll cannot spin the claim file or the store lock.
const minMCPInterval = 200 * time.Millisecond

// cmdMCP runs relay mcp: an MCP server over stdio a Claude Code planner
// spawns from its plugin manifest (docs/specs/2026-09-21-planner-channel-design.md).
// In channel mode it also claims its pane and drains its mailbox; in tools
// mode it only serves the four verbs as tools.
func cmdMCP(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	paneFlag := fs.String("planner", "", plannerFlagUsage)
	modeFlag := fs.String("mode", "auto", "channel|tools|auto (default: detected from the parent process's argv)")
	interval := fs.Duration("interval", time.Second, "poll interval in channel mode (floored at 200ms)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	// SPIKE(planner-id): the channel claim is keyed by this id; it was
	// the multiplexer pane id (--pane).
	pane := plannerID(*paneFlag)
	if pane == "" {
		fmt.Fprintln(os.Stderr, "relay mcp: no planner (set RELAY_PLANNER or pass --planner)")
		return exitCodeErr{code: 2}
	}

	if *interval < minMCPInterval {
		*interval = minMCPInterval
	}

	mode, err := resolveMCPMode(*modeFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "relay mcp: %v\n", err)
		return exitCodeErr{code: 2}
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	version := buildVersion()
	fmt.Fprintf(os.Stderr, "relay mcp: planner %s mode %s\n", pane, mcpModeWord(mode))

	srv := &mcp.Server{
		Verbs:   &mcp.RelayVerbs{RT: rt, Pane: pane},
		Version: version,
		Log:     os.Stderr,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if mode == mcp.ModeChannel {
		srv.OnInitialized = func() {
			startMCPChannel(ctx, rt, pane, version, srv, *interval)
		}
	}

	serveErr := srv.Serve(ctx, os.Stdin, os.Stdout)

	if mode == mcp.ModeChannel && rt.Channels != nil {
		_ = rt.Channels.Remove(pane, os.Getpid())
	}

	if serveErr != nil && !errors.Is(serveErr, context.Canceled) {
		return serveErr
	}
	return nil
}

// resolveMCPMode turns --mode into an mcp.Mode: "channel" and "tools" are
// literal, "auto" reads the parent process's argv and falls back to
// ModeTools (with a reason) when it cannot (spec §7).
func resolveMCPMode(flagVal string) (mcp.Mode, error) {
	switch flagVal {
	case "channel":
		return mcp.ModeChannel, nil
	case "tools":
		return mcp.ModeTools, nil
	case "auto":
		argv, err := mcp.ParentArgv()
		if err != nil {
			fmt.Fprintf(os.Stderr, "relay mcp: cannot read parent argv (%v); tools-only mode\n", err)
			return mcp.ModeTools, nil
		}
		return mcp.DetectMode(argv), nil
	default:
		return mcp.ModeTools, fmt.Errorf("--mode must be channel, tools, or auto, got %q", flagVal)
	}
}

func mcpModeWord(m mcp.Mode) string {
	if m == mcp.ModeChannel {
		return "channel"
	}
	return "tools"
}

// startMCPChannel writes this process's initial claim and, on success,
// starts the poll loop. A refused claim (ErrClaimHeld) exits the process:
// Claude Code shows the server as failed and the planner keeps pane
// delivery, exactly as if relay mcp had never started (spec §3.2, §6).
func startMCPChannel(ctx context.Context, rt relay.Runtime, pane, version string, p relay.Pusher, interval time.Duration) {
	if rt.Channels == nil {
		fmt.Fprintln(os.Stderr, "relay mcp: no claim store configured; running tools-only")
		return
	}

	now := rt.Now()
	cwd, _ := os.Getwd()
	claim := relay.Claim{
		Pane:      pane,
		PID:       os.Getpid(),
		StartedAt: now,
		SeenAt:    now,
		CWD:       cwd,
		Version:   version,
	}
	if err := rt.Channels.Write(claim, now); err != nil {
		fmt.Fprintf(os.Stderr, "relay mcp: %v\n", err)
		if errors.Is(err, relay.ErrClaimHeld) {
			os.Exit(1)
		}
		return
	}

	go pollMCPChannel(ctx, rt, pane, claim, p, interval)
}

// pollMCPChannel is relay mcp's channel-mode poll loop: refresh the claim,
// then drain the pane's mailbox (spec §3.4). It never exits on a drain
// error -- only a stolen claim (ErrClaimHeld on refresh) stops it, leaving
// the tools still serving.
func pollMCPChannel(ctx context.Context, rt relay.Runtime, pane string, claim relay.Claim, p relay.Pusher, interval time.Duration) {
	st := &relay.DrainState{Pane: pane}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			claim.SeenAt = rt.Now()
			if err := rt.Channels.Write(claim, claim.SeenAt); err != nil {
				fmt.Fprintf(os.Stderr, "relay mcp: refresh claim: %v\n", err)
				if errors.Is(err, relay.ErrClaimHeld) {
					return
				}
				continue
			}

			res, err := relay.Drain(ctx, rt, st, p)
			if err != nil {
				fmt.Fprintf(os.Stderr, "relay mcp: drain: %v\n", err)
				continue
			}
			if res.Pushed > 0 || res.States > 0 || len(res.Failed) > 0 {
				fmt.Fprintf(os.Stderr, "relay mcp: drain pushed=%d states=%d failed=%v\n", res.Pushed, res.States, res.Failed)
			}
		}
	}
}
