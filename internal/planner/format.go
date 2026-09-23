package planner

// PlannerFormat is the format of the Record JSON this binary writes. It
// follows store.BindingFormat's rule: format 1 is today's shape and is stored
// as an *absent* "format" field, so a record written today stays byte-identical
// to one written before the field existed; from format 2 on the number is
// written.
//
// Bump it whenever Record's JSON shape changes. An older relay that meets a
// newer format refuses to write, because its rewrite would erase every field
// it does not know (#372).
const PlannerFormat = 1

// storedFormat is the number actually written for a known format: 1 is stored
// as 0 so that the "format" key is omitted, and every other format is written
// as itself.
func storedFormat(n int) int {
	if n == 1 {
		return 0
	}
	return n
}
