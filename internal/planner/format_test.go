package planner

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/jsonshape"
	"github.com/fuad-daoud/relevo/internal/store"
)

// update rewrites the golden file, but only when PlannerFormat was bumped:
// see goldenDecision.
var update = flag.Bool("update", false, "rewrite testdata/record-shape.golden when PlannerFormat was bumped")

// recordGoldenPath is the golden file TestRecordShapeMatchesFormat compares
// against, and the one -update rewrites.
const recordGoldenPath = "testdata/record-shape.golden"

// The messages the planner record's golden test fails with, the same rules as
// internal/store's TestBindingShapeMatchesFormat.
const (
	recordShapeMsg   = "planner.Record's JSON shape changed: bump planner.PlannerFormat, then run go test ./internal/planner -run TestRecordShapeMatchesFormat -update"
	recordRefusalMsg = "bump planner.PlannerFormat first; an older relevo would erase the new fields"
	recordStaleMsg   = "planner.Record's golden format line is stale; run go test ./internal/planner -run TestRecordShapeMatchesFormat -update"
)

// recordGolden is the parsed golden: its format line and its key paths.
type recordGolden struct {
	format int
	keys   []string
}

// parseRecordGolden reads the golden file format: "format <N>" on the first
// line, then one key path per line.
func parseRecordGolden(raw []byte) (recordGolden, error) {
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) == 0 || lines[0] == "" {
		return recordGolden{}, errors.New("golden file is empty")
	}
	rest, ok := strings.CutPrefix(lines[0], "format ")
	if !ok {
		return recordGolden{}, fmt.Errorf("golden first line = %q, want \"format <N>\"", lines[0])
	}
	n, err := strconv.Atoi(strings.TrimSpace(rest))
	if err != nil {
		return recordGolden{}, fmt.Errorf("golden format line: %w", err)
	}
	return recordGolden{format: n, keys: lines[1:]}, nil
}

// marshalRecordGolden writes the golden file format.
func marshalRecordGolden(format int, keys []string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "format %d\n", format)
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

// goldenDecision is the -update rule as a pure function: write rewrites the
// golden and a non-empty msg fails the test instead. same reports whether the
// key paths already match the golden.
func goldenDecision(goldenFormat, codeFormat int, same bool) (write bool, msg string) {
	if same && goldenFormat == codeFormat {
		return false, ""
	}
	if codeFormat > goldenFormat {
		return true, ""
	}
	return false, recordRefusalMsg
}

// checkRecordShape compares keys against the golden and returns the message to
// fail with, or "" when all is well. When update is true it rewrites the
// golden whenever goldenDecision allows it.
func checkRecordShape(g recordGolden, codeFormat int, keys []string, update bool) string {
	same := slices.Equal(g.keys, keys)
	if update {
		write, msg := goldenDecision(g.format, codeFormat, same)
		if msg != "" {
			return msg
		}
		if write {
			if err := os.WriteFile(recordGoldenPath, marshalRecordGolden(codeFormat, keys), 0o644); err != nil {
				return fmt.Sprintf("write %s: %v", recordGoldenPath, err)
			}
			return ""
		}
	}
	if !same {
		return recordShapeMsg
	}
	if g.format != codeFormat {
		if g.format > codeFormat {
			return recordRefusalMsg
		}
		return recordStaleMsg
	}
	return ""
}

// readRecordGolden reads the golden, treating a missing file as the
// pre-creation state: format 0 and no keys.
func readRecordGolden(t *testing.T) recordGolden {
	t.Helper()
	raw, err := os.ReadFile(recordGoldenPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return recordGolden{}
		}
		t.Fatalf("read %s: %v", recordGoldenPath, err)
	}
	g, err := parseRecordGolden(raw)
	if err != nil {
		t.Fatalf("%s: %v", recordGoldenPath, err)
	}
	return g
}

// TestRecordShapeMatchesFormat pins Record's JSON shape against the golden
// file: a new field is a new key path, and that fails until PlannerFormat is
// bumped and the golden regenerated.
func TestRecordShapeMatchesFormat(t *testing.T) {
	g := readRecordGolden(t)
	keys := jsonshape.Keys(reflect.TypeOf(Record{}))

	if msg := checkRecordShape(g, PlannerFormat, keys, *update); msg != "" {
		t.Fatal(msg)
	}
	if got := readRecordGolden(t); got.format != PlannerFormat {
		t.Errorf("golden format = %d, want PlannerFormat %d", got.format, PlannerFormat)
	}
}

// TestRecordShapeFailurePath exercises the golden test's failure path without
// mutating the real type: a struct with one extra field fails with the bump
// message, and -update without a bump refuses.
func TestRecordShapeFailurePath(t *testing.T) {
	type recordWithANewField struct {
		Record
		BrandNew string `json:"brand_new"`
	}
	g := readRecordGolden(t)
	keys := jsonshape.Keys(reflect.TypeOf(recordWithANewField{}))

	if slices.Equal(g.keys, keys) {
		t.Fatal("an extra field must change the key paths")
	}
	if msg := checkRecordShape(g, PlannerFormat, keys, false); msg != recordShapeMsg {
		t.Errorf("mismatch message = %q, want %q", msg, recordShapeMsg)
	}
	if msg := checkRecordShape(g, PlannerFormat, keys, true); msg != recordRefusalMsg {
		t.Errorf("-update without a bump = %q, want %q", msg, recordRefusalMsg)
	}
}

func TestStoredFormat(t *testing.T) {
	if got := storedFormat(PlannerFormat); got != 0 {
		t.Errorf("storedFormat(%d) = %d, want 0: format 1 is absent on disk", PlannerFormat, got)
	}
	if got := storedFormat(2); got != 2 {
		t.Errorf("storedFormat(2) = %d, want 2", got)
	}
}

// TestRegistryWriteRefusesANewerFormat pins the planner half of §4.1: a record
// written by a newer relevo loads, and writing it back is refused with
// ErrNewerFormat, leaving the file byte-for-byte as it was.
func TestRegistryWriteRefusesANewerFormat(t *testing.T) {
	reg := testRegistry(t)
	rec := record("pl_aaaaaaaaaaaa", "alpha", "claude", "sess-1", 0)
	rec.Format = PlannerFormat + 1

	if err := os.MkdirAll(reg.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(reg.Root, rec.ID+".json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := reg.Get(rec.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Format != PlannerFormat+1 {
		t.Fatalf("loaded Format = %d, want %d", got.Format, PlannerFormat+1)
	}

	_, err = reg.SetHost(rec.ID, 42, 7)
	var newer *store.ErrNewerFormat
	if !errors.As(err, &newer) {
		t.Fatalf("SetHost on a newer format = %v, want *store.ErrNewerFormat", err)
	}
	if !errors.Is(err, store.ErrNewerFormatSentinel) {
		t.Errorf("errors.Is(%v, ErrNewerFormatSentinel) = false, want true", err)
	}
	if newer.Kind != "planner record" || newer.Name != rec.Name || newer.Have != PlannerFormat+1 || newer.Know != PlannerFormat {
		t.Errorf("ErrNewerFormat = %+v", newer)
	}
	wantText := `planner record "alpha" was written by a newer relevo (format 2; this relevo knows 1): upgrade relevo; a planner session reconnects relevo mcp with /mcp`
	if err.Error() != wantText {
		t.Errorf("ErrNewerFormat text = %q, want %q", err.Error(), wantText)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, after) {
		t.Error("a refused write must leave the record byte-identical")
	}
}
