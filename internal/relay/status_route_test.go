package relay

import (
	"context"
	"encoding/json"
	"testing"
)

// routeDoc is the part of the status JSON the plan's §4 requires: the two new
// route fields, and the absence of the pane-era fields.
type routeDoc struct {
	Bindings []struct {
		Name             string `json:"name"`
		PlannerRoute     string `json:"planner_route"`
		PlannerRouteLive bool   `json:"planner_route_live"`
		PlannerID        string `json:"planner_id"`
	} `json:"bindings"`
}

// TestStatusJSONHasRouteFields is the plan's required case for §3.6: a
// tools-mode planner (no live claim, no deliverer) reads planner_route=pull
// with planner_route_live false, and a live channel claim reads channel with
// planner_route_live true.
func TestStatusJSONHasRouteFields(t *testing.T) {
	rt := routeRuntime(t)
	seedPending(t, rt, "webshop", "pl_aaaaaaaabbbb", "claude")

	doc := statusDoc(t, rt)
	if len(doc.Bindings) != 1 {
		t.Fatalf("status has %d rows, want 1", len(doc.Bindings))
	}
	if doc.Bindings[0].PlannerRoute != "pull" {
		t.Errorf("planner_route = %q, want pull for a tools-mode planner", doc.Bindings[0].PlannerRoute)
	}
	if doc.Bindings[0].PlannerRouteLive {
		t.Error("planner_route_live must be false for the pull route")
	}
	if doc.Bindings[0].PlannerID != "pl_aaaaaaaabbbb" {
		t.Errorf("planner_id = %q, want the binding's planner id", doc.Bindings[0].PlannerID)
	}

	// A live channel claim turns the same row into the channel route.
	rt.Channels = fakeClaimStore{"pl_aaaaaaaabbbb": &Claim{Planner: "pl_aaaaaaaabbbb", PID: 1}}
	doc = statusDoc(t, rt)
	if doc.Bindings[0].PlannerRoute != "channel" {
		t.Errorf("planner_route = %q, want channel with a live claim", doc.Bindings[0].PlannerRoute)
	}
	if !doc.Bindings[0].PlannerRouteLive {
		t.Error("planner_route_live must be true for a live channel")
	}
}

// TestStatusJSONOmitsPaneEraFields keeps the removal honest: the fields §4
// deletes must not reappear in the JSON.
func TestStatusJSONOmitsPaneEraFields(t *testing.T) {
	rt := routeRuntime(t)
	seedPending(t, rt, "webshop", "pl_aaaaaaaabbbb", "claude")

	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := doc["herdr_error"]; ok {
		t.Error("herdr_error must be gone from the status JSON")
	}
	rows, _ := doc["bindings"].([]any)
	if len(rows) == 0 {
		t.Fatal("no binding rows")
	}
	row, _ := rows[0].(map[string]any)
	for _, gone := range []string{"planner_pane", "planner_status", "planner_focused", "workspace", "builder_pane", "foreign", "sub_agents", "hold"} {
		if _, ok := row[gone]; ok {
			t.Errorf("%s must be gone from a status row", gone)
		}
	}
}

// statusDoc marshals a Status report and reads back the route fields.
func statusDoc(t *testing.T, rt Runtime) routeDoc {
	t.Helper()
	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var doc routeDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return doc
}
