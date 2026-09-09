package relay

import (
	"strings"
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

func TestBuilderDiagnosisDetail(t *testing.T) {
	const moved = ", and the builder was never session-identified, " +
		"so it may be alive in a moved pane -- verify before rebinding"

	tests := []struct {
		name  string
		d     BuilderDiagnosis
		round int
		want  string
	}{
		{
			name:  "round open, identified",
			d:     BuilderDiagnosis{RoundOpen: true, SessionIdentified: true},
			round: 3,
			want:  "round 3 was open -- that work is unaccounted for; rebind and resend the round",
		},
		{
			name:  "round open, unidentified",
			d:     BuilderDiagnosis{RoundOpen: true},
			round: 3,
			want:  "round 3 was open -- that work is unaccounted for" + moved,
		},
		{
			name:  "report delivered, identified",
			d:     BuilderDiagnosis{SessionIdentified: true},
			round: 3,
			want:  "round 2 report delivered; nothing outstanding -- unless you want another round",
		},
		{
			name:  "report delivered, unidentified",
			d:     BuilderDiagnosis{},
			round: 3,
			want:  "round 2 report delivered; nothing outstanding" + moved,
		},
		{
			name:  "never sent, identified",
			d:     BuilderDiagnosis{SessionIdentified: true},
			round: 1,
			want:  "no round has been sent yet; nothing outstanding -- unless you want to send one",
		},
		{
			name:  "never sent, unidentified",
			d:     BuilderDiagnosis{},
			round: 1,
			want:  "no round has been sent yet; nothing outstanding" + moved,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.d.Detail(tc.round)
			if got != tc.want {
				t.Errorf("Detail(%d) =\n  %q\nwant\n  %q", tc.round, got, tc.want)
			}
		})
	}
}

// A binding bound but never sent has Round 1, and queueReport's increment
// means the "delivered report" wording would name round 0 -- a report that
// does not exist. That binding is also the one whose builder has taken no
// turn, so it is exactly the unidentified case this detail exists to warn
// about; it must not be papered over with a nonsense round number.
func TestDetailNeverNamesRoundZero(t *testing.T) {
	for _, d := range []BuilderDiagnosis{{}, {SessionIdentified: true}} {
		got := d.Detail(1)
		if strings.Contains(got, "round 0") {
			t.Errorf("Detail(1) = %q, must not name round 0", got)
		}
		if got == "" {
			t.Error("Detail must be total, got empty string")
		}
	}
}

func TestDetailIsTotal(t *testing.T) {
	for _, round := range []int{0, 1, 2, 7} {
		for _, open := range []bool{true, false} {
			for _, sess := range []bool{true, false} {
				d := BuilderDiagnosis{RoundOpen: open, SessionIdentified: sess}
				if d.Detail(round) == "" {
					t.Errorf("Detail(%d) empty for %+v", round, d)
				}
			}
		}
	}
}
