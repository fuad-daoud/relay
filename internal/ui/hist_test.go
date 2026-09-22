package ui

import (
	"context"
	"testing"
	"time"

	"github.com/fuad-daoud/relay/internal/relay"
)

// histFixtureDir is the ingest package's own golden fixture -- three
// rounds (reported, halted, exited) -- reused read-only here exactly as
// internal/relay's Show tests reuse it (Task 4's plan: seed a db by
// ingest.Ingest over a copy packed as a tarball, so the binding is
// archived and not live).
const histFixtureDir = "../ingest/testdata/binding-three-rounds"

var histFixtureBindFiles = map[string]bool{
	"bind-legacy.json":   true,
	"bind-round4.json":   true,
	"bind-cwd-gone.json": true,
}

// seedArchivedHistBinding packs the golden fixture into a tarball and
// ingests it as an archive source, so the binding it produces carries
// ArchivedAt and is never in a live report -- fetchShow and relay.Show
// fall through to the db for it by construction. It returns a runtime
// carrying that db and the one HistoryBinding row relay.Bindings gives
// back for it.
func histModel(t *testing.T, rt relay.Runtime, h relay.HistoryBinding) Model {
	t.Helper()
	m := newModel(context.Background(), plannerSource{rt}, Options{Interval: time.Millisecond})
	m.width, m.height, m.ready = 140, 40, true
	m.scope = scopeAll
	m.statusLoaded = true
	m, cmd := m.pointDetailAtHist(h)
	if cmd != nil {
		res, _ := m.Update(cmd())
		m = res.(Model)
	}
	return m
}

// TestPointAtArchivedRowLoadsPlanFromDB pins the hist branch of "enter /
// re-point on a row" (#183, §5.8): pointing at a hist row sets live false,
// round the newest (every round closed), and the plan tab's fetch reads
// the database, not a file.
// TestArchivedTerminalTabShowsTranscriptRows pins fetchShow's terminal
// routing (terminal -> ShowTranscript) and the "never tail-following" rule
// for a hist row's terminal (#183, §5.8): round 2 of the fixture has a
// builder.log (transcript rows via logOnlyTranscriptRecords, per
// internal/ingest), so stepping to it and opening the terminal tab renders
// transcript content, not a live capture. (Round 3, the default newest
// round, has neither a stream nor a log and would read Missing instead.)
// TestArchivedMissingDiffIsEmptyProse pins §6: a missing artifact (round 3
// of the fixture has no diff.patch) renders as tabContent.empty prose, not
// as an error.
// TestDetailHeaderArchived pins detailHeader's exact rendering for an
// archived binding.
// TestArchivedStepRoundRefetches pins round stepping for a hist row
// (#183): "[" moves detail.round within a closed binding's rounds exactly
// as it does for a live one, invalidating every tab's cache.
