package jsonshape

import (
	"reflect"
	"slices"
	"testing"
)

func TestKeys(t *testing.T) {
	got := Keys(reflect.TypeOf(fixture{}))
	want := []string{
		"NoTag", // tagless: the Go field name, from the embedded struct
		"Plain", // tagless: the Go field name
		"items[].NoTag",
		"items[].tagged",
		"leaf", // a MarshalJSON type is a leaf
		"lookup{}.NoTag",
		"lookup{}.tagged",
		"ptr.NoTag",
		"ptr.tagged",
		"renamed_tag", // the tag name, not the Go name
		"tagged",
		"when", // time.Time is a leaf
	}
	if !slices.Equal(got, want) {
		t.Errorf("Keys(fixture{}) =\n%q\nwant\n%q", got, want)
	}
}

// TestKeysSkipsTheDashField pins the other half of the "-" rule: a field
// tagged json:"-" contributes no path at all.
func TestKeysSkipsTheDashField(t *testing.T) {
	for _, key := range Keys(reflect.TypeOf(fixture{})) {
		if key == "Skipped" || key == "skipped" {
			t.Errorf(`a json:"-" field must contribute no key, got %q`, key)
		}
	}
}
