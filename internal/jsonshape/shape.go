// Package jsonshape derives the JSON key paths a Go struct encodes to. The
// format numbers in internal/store and internal/planner guard the shape those
// paths describe: a new field is a shape change, and these paths are what the
// golden tests compare to notice one.
package jsonshape

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"time"
)

// Keys returns every JSON key path t encodes to, sorted and unique.
//
// The paths follow encoding/json's field selection:
//   - a field tagged `json:"-"` is skipped;
//   - a field's tag name is used, or its Go name when the tag names nothing
//     (an option like omitempty is not a name);
//   - an embedded struct is flattened, as encoding/json promotes its fields;
//   - pointers, slices, arrays and structs recurse, a slice or array adding
//     "[]" to the path and a map adding "{}", so a map of structs is
//     "name{}.field";
//   - time.Time and any type implementing json.Marshaler is a leaf.
//
// A struct with a new field therefore yields a new path, which is exactly what
// the format guard's golden test must catch.
func Keys(t reflect.Type) []string {
	set := map[string]struct{}{}
	walk(t, "", set)

	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// timeType is the one type treated as a leaf besides json.Marshaler.
var timeType = reflect.TypeOf(time.Time{})

// marshalerType is json.Marshaler's interface type.
var marshalerType = reflect.TypeOf((*json.Marshaler)(nil)).Elem()

// isLeaf reports whether t encodes as one JSON value, not as an object to
// recurse into.
func isLeaf(t reflect.Type) bool {
	return t == timeType || t.Implements(marshalerType)
}

// walk collects the key paths t contributes under prefix.
func walk(t reflect.Type, prefix string, set map[string]struct{}) {
	if isLeaf(t) {
		addKey(prefix, set)
		return
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if isLeaf(t) {
		addKey(prefix, set)
		return
	}

	switch t.Kind() {
	case reflect.Struct:
		walkStruct(t, prefix, set)
	case reflect.Slice, reflect.Array:
		walk(t.Elem(), prefix+"[]", set)
	case reflect.Map:
		walk(t.Elem(), prefix+"{}", set)
	default:
		// Every other kind -- strings, numbers, bools, interfaces, funcs --
		// is a leaf value.
		addKey(prefix, set)
	}
}

// walkStruct walks t's exported fields, and its embedded fields as
// encoding/json does: an anonymous field with no tag name is flattened into
// the parent's namespace.
func walkStruct(t reflect.Type, prefix string, set map[string]struct{}) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" && !f.Anonymous {
			continue // unexported and not embedded
		}

		name := strings.SplitN(f.Tag.Get("json"), ",", 2)[0]
		switch {
		case name == "-":
			continue
		case f.Anonymous && name == "":
			walk(f.Type, prefix, set)
		default:
			if name == "" {
				name = f.Name
			}
			walk(f.Type, join(prefix, name), set)
		}
	}
}

// join extends prefix with one named field.
func join(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}

// addKey records a terminal path. An empty prefix is the type passed to Keys
// itself: only a struct has paths, so there is nothing to record.
func addKey(prefix string, set map[string]struct{}) {
	if prefix == "" {
		return
	}
	set[prefix] = struct{}{}
}
