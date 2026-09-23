//go:build !unix

package migrate

// sameFS reports whether a and b live on the same filesystem. Outside unix the
// check is not implemented, so the answer is always true and the refusal never
// fires on a platform where it cannot be made.
func sameFS(a, b string) bool { return true }
