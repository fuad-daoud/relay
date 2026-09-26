package mcp

import (
	"context"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

func TestSendToolSchemaHasBuilderProperty(t *testing.T) {
	for _, tool := range Tools() {
		if tool.Name != "send" {
			continue
		}
		props, ok := tool.InputSchema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("send schema properties = %#v, want a map", tool.InputSchema["properties"])
		}
		prop, ok := props["builder"].(map[string]any)
		if !ok {
			t.Fatalf("send schema has no builder property: %#v", props)
		}
		if prop["type"] != "string" {
			t.Errorf("builder property type = %v, want string", prop["type"])
		}
		return
	}
	t.Fatal("no send tool in Tools()")
}

// TestRelevoVerbsSendPassesBuilder: a dry run reports the new candidate; the same call without it reports the binding's own.
func TestRelevoVerbsSendPassesBuilder(t *testing.T) {
	s := store.New(t.TempDir())
	set := writeCandidates(t, `[
		{"harness":"agy","provider":"test","model":"m","roles":["builder"],"extra_args":["--dangerously-skip-permissions"]},
		{"harness":"claude","provider":"test","model":"m","roles":["builder"]}
	]`)
	rt := relevo.Runtime{
		Store:      s,
		Candidates: set,
		Runner:     stubRunner{},
		Gates:      testGateKV(t),
		Now:        func() time.Time { return time.Unix(0, 0) },
	}
	saveVerbBinding(t, s, store.Binding{
		Name: "webshop", CWD: "/repo",
		Planner:          store.Endpoint{PaneID: "w2:p3"},
		Builder:          store.Endpoint{Mode: store.ModeHeadless},
		BuilderCandidate: "agy/test/m",
		Round:            1, State: store.StateActive,
	})

	v := &RelevoVerbs{RT: rt, Planner: mcpTestPlannerA}
	plan := writeTempPlan(t, "# do the thing")

	res, err := v.Send(context.Background(), SendArgs{Name: "webshop", File: plan, Builder: "claude/test/m", DryRun: true})
	if err != nil {
		t.Fatalf("Send dry-run with builder: %v", err)
	}
	d, ok := res.(relevo.DryRun)
	if !ok {
		t.Fatalf("result = %#v, want relevo.DryRun", res)
	}
	if d.Candidate != "claude/test/m" {
		t.Errorf("Candidate = %q, want claude/test/m (Send must pass Builder through)", d.Candidate)
	}

	// Control: without a builder, the binding's own candidate comes back.
	res, err = v.Send(context.Background(), SendArgs{Name: "webshop", File: plan, DryRun: true})
	if err != nil {
		t.Fatalf("Send dry-run without builder: %v", err)
	}
	d, ok = res.(relevo.DryRun)
	if !ok {
		t.Fatalf("result = %#v, want relevo.DryRun", res)
	}
	if d.Candidate != "agy/test/m" {
		t.Errorf("Candidate = %q, want agy/test/m without a builder", d.Candidate)
	}
}
