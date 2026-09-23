package ingest

import (
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
)

func TestRoundFactsFromFixtureRound1(t *testing.T) {
	ts := func(s string) time.Time {
		tm, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatalf("parse %s: %v", s, err)
		}
		return tm
	}

	events := []store.LogEntry{
		{TS: ts("2026-09-10T10:00:00Z"), Round: 1, Kind: store.KindPick},
		{TS: ts("2026-09-10T10:00:01Z"), Round: 1, Kind: store.KindPlan, Tier: "high"},
		{TS: ts("2026-09-10T10:00:02Z"), Round: 1, Kind: store.KindDiff, Commits: 2, Tree: "clean"},
		{
			TS: ts("2026-09-10T10:00:03Z"), Round: 1, Kind: store.KindReport, Outcome: "done",
			Gate: &store.GateRecord{Command: "make check", Result: "pass", ExitCode: 0, DurationMS: 900, LogPath: "001-gate.log"},
			Usage: &usage.Usage{
				Harness: "opencode", Provider: "openrouter", Model: "z-ai/glm-5.3-flash",
				Tokens:  usage.Tokens{In: 1000, CacheRead: 5000, CacheWrite: 0, Out: 200},
				Cost:    usage.Cost{USD: 0.12, Basis: usage.Measured},
				Samples: 1,
			},
		},
		// Round 2's entries must not leak into round 1's facts.
		{TS: ts("2026-09-10T10:01:00Z"), Round: 2, Kind: store.KindPlan, Tier: "low"},
	}

	r := roundFacts(events, 1)

	if !r.StartedAt.Equal(ts("2026-09-10T10:00:01Z")) {
		t.Errorf("StartedAt = %v, want the plan entry's ts", r.StartedAt)
	}
	if r.ClosedAt == nil || !r.ClosedAt.Equal(ts("2026-09-10T10:00:03Z")) {
		t.Errorf("ClosedAt = %v, want the report entry's ts", r.ClosedAt)
	}
	if r.Tier == nil || *r.Tier != "high" {
		t.Errorf("Tier = %v, want high", r.Tier)
	}
	if r.Commits == nil || *r.Commits != 2 {
		t.Errorf("Commits = %v, want 2", r.Commits)
	}
	if r.Tree == nil || *r.Tree != "clean" {
		t.Errorf("Tree = %v, want clean", r.Tree)
	}
	if r.GateResult == nil || *r.GateResult != "pass" {
		t.Errorf("GateResult = %v, want pass", r.GateResult)
	}
	if r.GateExit == nil || *r.GateExit != 0 {
		t.Errorf("GateExit = %v, want 0", r.GateExit)
	}
	if r.GateDurationMS == nil || *r.GateDurationMS != 900 {
		t.Errorf("GateDurationMS = %v, want 900", r.GateDurationMS)
	}
	if r.InTokens == nil || *r.InTokens != 1000 {
		t.Errorf("InTokens = %v, want 1000", r.InTokens)
	}
	if r.CacheTokens == nil || *r.CacheTokens != 5000 {
		t.Errorf("CacheTokens = %v, want 5000", r.CacheTokens)
	}
	if r.WriteTokens == nil || *r.WriteTokens != 0 {
		t.Errorf("WriteTokens = %v, want 0", r.WriteTokens)
	}
	if r.OutTokens == nil || *r.OutTokens != 200 {
		t.Errorf("OutTokens = %v, want 200", r.OutTokens)
	}
	if r.CostUSD == nil || *r.CostUSD != 0.12 {
		t.Errorf("CostUSD = %v, want 0.12", r.CostUSD)
	}
	if r.CostBasis == nil || *r.CostBasis != "measured" {
		t.Errorf("CostBasis = %v, want measured", r.CostBasis)
	}
	if r.ReportOutcome == nil || *r.ReportOutcome != "done" {
		t.Errorf("ReportOutcome = %v, want done", r.ReportOutcome)
	}
}
