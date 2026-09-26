package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func rm(s string) json.RawMessage { return json.RawMessage(s) }

func TestDiffDocsSections(t *testing.T) {
	t.Parallel()

	a := Doc{
		Candidates: rm(`[{"harness":"claude"}]`),
		Roles:      rm(`{}`),
	}
	b := Doc{
		Roles:  rm(`{}`),
		Policy: rm(`{"max_switches":1}`),
	}

	got := DiffDocs(a, b)
	want := []Change{
		{Path: "candidates", Op: "remove", Before: rm(`[{"harness":"claude"}]`)},
		{Path: "policy", Op: "add", After: rm(`{"max_switches":1}`)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DiffDocs = %#v, want %#v", got, want)
	}

	// The unchanged roles section contributes nothing, and the reverse
	// direction swaps the add and the remove.
	back := DiffDocs(b, a)
	wantBack := []Change{
		{Path: "candidates", Op: "add", After: rm(`[{"harness":"claude"}]`)},
		{Path: "policy", Op: "remove", Before: rm(`{"max_switches":1}`)},
	}
	if !reflect.DeepEqual(back, wantBack) {
		t.Errorf("DiffDocs reverse = %#v, want %#v", back, wantBack)
	}
}

func TestDiffDocsNested(t *testing.T) {
	t.Parallel()

	a := Doc{Policy: rm(`{"max_switches":2,"order":{"builder":["x","y"]},` +
		`"nested":{"keep":1,"drop":2,"dotted.key":1,"change":"a"},"arr":[1,2,3],"kind":1}`)}
	b := Doc{Policy: rm(`{"max_switches":3,"order":{"builder":["x","z","w"]},` +
		`"nested":{"keep":1,"add":2,"dotted.key":2,"change":"b"},"arr":[1,9],"kind":"one"}`)}

	got := DiffDocs(a, b)
	want := []Change{
		{Path: "policy.arr[1]", Op: "change", Before: rm(`2`), After: rm(`9`)},
		{Path: "policy.arr[2]", Op: "remove", Before: rm(`3`)},
		{Path: "policy.kind", Op: "change", Before: rm(`1`), After: rm(`"one"`)},
		{Path: "policy.max_switches", Op: "change", Before: rm(`2`), After: rm(`3`)},
		{Path: "policy.nested.add", Op: "add", After: rm(`2`)},
		{Path: "policy.nested.change", Op: "change", Before: rm(`"a"`), After: rm(`"b"`)},
		{Path: `policy.nested["dotted.key"]`, Op: "change", Before: rm(`1`), After: rm(`2`)},
		{Path: "policy.nested.drop", Op: "remove", Before: rm(`2`)},
		{Path: "policy.order.builder[1]", Op: "change", Before: rm(`"y"`), After: rm(`"z"`)},
		{Path: "policy.order.builder[2]", Op: "add", After: rm(`"w"`)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DiffDocs =\n%#v\nwant\n%#v", got, want)
	}
}

func TestDiffDocsEqualIsEmpty(t *testing.T) {
	t.Parallel()

	doc := Doc{
		Candidates: rm(`[{"a":1}]`),
		Policy:     rm(`{"b":{"c":[1,2,{"d":"e"}]}}`),
	}
	got := DiffDocs(doc, doc)
	if got == nil {
		t.Fatal("DiffDocs(equal) = nil, want an empty non-nil slice")
	}
	if len(got) != 0 {
		t.Errorf("DiffDocs(equal) = %#v, want empty", got)
	}
}

func TestDescribe(t *testing.T) {
	t.Parallel()

	cases := []struct {
		change Change
		want   string
	}{
		{Change{Path: "policy.max_switches", Op: "change", Before: rm(`2`), After: rm(`3`)},
			`~ policy.max_switches  2 → 3`},
		{Change{Path: "roles", Op: "add", After: rm(`{"builder":["x"]}`)},
			`+ roles  {"builder":["x"]}`},
		{Change{Path: "servers", Op: "remove", Before: rm(`{"zen":{"url":"x"}}`)},
			`- servers  {"zen":{"url":"x"}}`},
		{Change{Path: "secret.typesafe", Op: "set"},
			`* secret.typesafe`},
	}
	for _, tc := range cases {
		if got := Describe(tc.change); got != tc.want {
			t.Errorf("Describe(%+v) = %q, want %q", tc.change, got, tc.want)
		}
	}

	// A value over maxValue runes is cut to maxValue runes, marked with an
	// ellipsis.
	long := rm(`"` + strings.Repeat("a", 100) + `"`)
	got := Describe(Change{Path: "p", Op: "add", After: long})
	want := "+ p  " + string([]rune(string(long))[:maxValue-1]) + "…"
	if got != want {
		t.Errorf("Describe(long) = %q, want %q", got, want)
	}
	if n := len([]rune(got)); n != len("+ p  ")+maxValue {
		t.Errorf("Describe(long) rune length = %d, want %d", n, len("+ p  ")+maxValue)
	}
}
