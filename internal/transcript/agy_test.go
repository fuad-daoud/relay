package transcript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestAgyErrorResults renders the 7 real agy ERROR results (§7.2 N1): the
// limit text agy carries in result.error reaches the log as an "error: " line
// after the status and before the response, so a scan of the rendered stream
// finds it. Before this, renderAgy printed only "result: ERROR" and dropped
// the text.
func TestAgyErrorResults(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "agy-errors", "results.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != 7 {
		t.Fatalf("fixture has %d lines, want 7", len(lines))
	}
	for i, line := range lines {
		var obj struct {
			Result struct {
				Status   string `json:"status"`
				Response string `json:"response"`
				Error    string `json:"error"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Fatalf("fixture line %d: %v", i+1, err)
		}
		if obj.Result.Status != "ERROR" || obj.Result.Error == "" {
			t.Fatalf("fixture line %d is not an ERROR result with an error field: %+v", i+1, obj.Result)
		}
		var want []string
		want = append(want, "result: ERROR")
		want = append(want, "error: "+obj.Result.Error)
		if obj.Result.Response != "" {
			want = append(want, obj.Result.Response)
		}
		if got := Render("agy", []byte(line)); !reflect.DeepEqual(got, want) {
			t.Errorf("line %d:\n got %q\nwant %q", i+1, got, want)
		}
	}

	// A result with no error renders exactly as before: the status line, then
	// the response.
	noError := `{"event":"result","result":{"status":"ERROR","response":"could not continue","denied_actions":[]}}`
	want := []string{"result: ERROR", "could not continue"}
	if got := Render("agy", []byte(noError)); !reflect.DeepEqual(got, want) {
		t.Errorf("a result with no error = %q, want %q", got, want)
	}
}
