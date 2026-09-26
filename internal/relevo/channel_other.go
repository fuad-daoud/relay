//go:build !unix

package relevo

// defaultClaimAlive: relevo targets Linux and macOS; on other platforms no
// claim is ever live, so the daemon always delivers to the pane.
func defaultClaimAlive(pid int) bool {
	return false
}
