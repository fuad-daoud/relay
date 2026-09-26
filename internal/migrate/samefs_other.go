//go:build !unix

package migrate

// sameFS always answers true: outside unix the check is not implemented, so
// the refusal it guards never fires.
func sameFS(a, b string) bool { return true }
