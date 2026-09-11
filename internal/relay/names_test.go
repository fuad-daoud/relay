package relay

import (
	"testing"

	"github.com/fuad-daoud/relay/internal/alias"
)

// TestConsultRolesTooLong pins the boundary the note turns on: a name one
// character under the derived-name limit fits, one over does not, and a table
// with no consult roles can never name a role at all.
func TestConsultRolesTooLong(t *testing.T) {
	tbl := consultTable(t)

	// 14 + 1 + 8 + 1 + 8 = 32: exactly herdr's limit, so reviewer fits.
	if got := ConsultRolesTooLong(tbl, "abcdefghijklmn"); len(got) != 0 {
		t.Errorf("a 14-character binding name fits reviewer: got %v, want empty", got)
	}

	// 15 + 1 + 8 + 1 + 8 = 33: one over.
	if got := ConsultRolesTooLong(tbl, "abcdefghijklmno"); len(got) != 1 || got[0] != "reviewer" {
		t.Errorf("a 15-character binding name is one over for reviewer: got %v, want [reviewer]", got)
	}

	// The shipped table has only builder aliases: nothing can be too long.
	if got := ConsultRolesTooLong(alias.DefaultTable(), "abcdefghijklmnopqrstuvwxy"); len(got) != 0 {
		t.Errorf("a table with no consult roles: got %v, want empty", got)
	}
}
