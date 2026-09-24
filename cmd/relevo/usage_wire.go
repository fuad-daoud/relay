package main

import (
	// usagepkg: package main already has a package-level const named
	// "usage" (the help text in main.go), so internal/usage is imported
	// under a name that does not shadow it.
	usagepkg "github.com/fuad-daoud/relevo/internal/usage"
)

// newUsageReader wires the usage reader and the price table from the loaded
// config. The reader never errors, and the price table was validated on its
// way into the config store, so wiring it can never stop a round from closing.
func newUsageReader(prices usagepkg.Prices) (usagepkg.Reader, usagepkg.Prices) {
	return usagepkg.New(), prices
}
