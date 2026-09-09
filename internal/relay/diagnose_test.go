package relay

import (
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/store"
)

func TestDiagnoseBuilderDerivesBothFacts(t *testing.T) {
	sent := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		binding  store.Binding
		wantOpen bool
		wantSess bool
	}{
		{
			name: "round in flight and session recorded",
			binding: store.Binding{
				RoundStartedAt: sent,
				Builder:        store.Endpoint{PaneID: "w2:p4", SessionID: "sess-1"},
			},
			wantOpen: true,
			wantSess: true,
		},
		{
			name: "round closed and session recorded",
			binding: store.Binding{
				Builder: store.Endpoint{PaneID: "w2:p4", SessionID: "sess-1"},
			},
			wantOpen: false,
			wantSess: true,
		},
		{
			name: "round in flight and no session",
			binding: store.Binding{
				RoundStartedAt: sent,
				Builder:        store.Endpoint{PaneID: "w2:p4", Kind: "agy"},
			},
			wantOpen: true,
			wantSess: false,
		},
		{
			name: "round closed and no session",
			binding: store.Binding{
				Builder: store.Endpoint{PaneID: "w2:p4", Kind: "agy"},
			},
			wantOpen: false,
			wantSess: false,
		},
		{
			name:     "zero binding does not panic",
			binding:  store.Binding{},
			wantOpen: false,
			wantSess: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DiagnoseBuilder(tc.binding)
			if got.RoundOpen != tc.wantOpen {
				t.Errorf("RoundOpen = %v, want %v", got.RoundOpen, tc.wantOpen)
			}
			if got.SessionIdentified != tc.wantSess {
				t.Errorf("SessionIdentified = %v, want %v", got.SessionIdentified, tc.wantSess)
			}
		})
	}
}
