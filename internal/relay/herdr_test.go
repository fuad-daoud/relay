package relay

import (
	"testing"

	"github.com/fuad-daoud/relay/internal/herdr"
	"github.com/fuad-daoud/relay/internal/store"
)

func TestSameAgent(t *testing.T) {
	cases := []struct {
		name  string
		agent herdr.Agent
		ep    store.Endpoint
		want  bool
	}{
		{
			name:  "session matches, pane moved",
			agent: herdr.Agent{PaneID: "w2:p9", Kind: "agy", Session: herdr.Session{Value: "s1"}},
			ep:    store.Endpoint{PaneID: "w2:p4", Kind: "agy", SessionID: "s1"},
			want:  true,
		},
		{
			name:  "session recorded but gone, pane reused by a stranger",
			agent: herdr.Agent{PaneID: "w2:p4", Kind: "agy", Session: herdr.Session{Value: "s2"}},
			ep:    store.Endpoint{PaneID: "w2:p4", Kind: "agy", SessionID: "s1"},
			want:  false,
		},
		{
			name:  "no session, pane and kind match",
			agent: herdr.Agent{PaneID: "w2:p4", Kind: "agy"},
			ep:    store.Endpoint{PaneID: "w2:p4", Kind: "agy"},
			want:  true,
		},
		{
			name:  "no session, pane matches but kind differs",
			agent: herdr.Agent{PaneID: "w2:p4", Kind: "claude"},
			ep:    store.Endpoint{PaneID: "w2:p4", Kind: "agy"},
			want:  false,
		},
		{
			name:  "no session, no kind recorded, pane matches",
			agent: herdr.Agent{PaneID: "w2:p4", Kind: "claude"},
			ep:    store.Endpoint{PaneID: "w2:p4"},
			want:  true,
		},
		{
			name:  "no session, pane differs",
			agent: herdr.Agent{PaneID: "w2:p9", Kind: "agy"},
			ep:    store.Endpoint{PaneID: "w2:p4", Kind: "agy"},
			want:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SameAgent(tc.agent, tc.ep); got != tc.want {
				t.Fatalf("SameAgent = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFindAgentDoesNotFallBackWhenSessionRecorded(t *testing.T) {
	agents := []herdr.Agent{
		{PaneID: "w2:p4", Kind: "agy", Session: herdr.Session{Value: "stranger"}},
	}
	ep := store.Endpoint{PaneID: "w2:p4", Kind: "agy", SessionID: "mine"}

	if _, ok := FindAgent(agents, ep); ok {
		t.Fatal("FindAgent matched a stranger occupying the recorded pane")
	}
}
