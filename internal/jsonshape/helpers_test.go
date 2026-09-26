package jsonshape

import "time"

// marshalLeaf is the "a type with a MarshalJSON method" case: a leaf, whatever
// fields it has.
type marshalLeaf struct {
	N int
}

func (marshalLeaf) MarshalJSON() ([]byte, error) { return []byte(`{}`), nil }

// fixtureInner is what every recursion case below points at. Its fields cover
// a tagless field, an omitempty option and a "-" field.
type fixtureInner struct {
	NoTag   string
	Tagged  int    `json:"tagged,omitempty"`
	Skipped string `json:"-"`
}

// fixture exercises every rule Keys documents: a tagless field, a "-" field,
// an omitempty option, an embedded struct, a pointer, a slice of structs, a
// map of structs, a time.Time and a MarshalJSON leaf.
type fixture struct {
	fixtureInner
	Ptr     *fixtureInner           `json:"ptr"`
	Items   []fixtureInner          `json:"items"`
	Lookup  map[string]fixtureInner `json:"lookup"`
	When    time.Time               `json:"when"`
	Leaf    marshalLeaf             `json:"leaf"`
	Renamed string                  `json:"renamed_tag,omitempty"`
	Plain   string
}
