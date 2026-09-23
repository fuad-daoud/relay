package main

import (
	"fmt"
	"os"
	"path/filepath"

	// usagepkg: package main already has a package-level const named
	// "usage" (the help text in main.go), so internal/usage is imported
	// under a name that does not shadow it.
	usagepkg "github.com/fuad-daoud/relay/internal/usage"
)

// newUsageReader wires the usage reader and the price table from the
// user's config directory. The reader never errors; a bad price file says
// so once on stderr and runs on the embedded default, so wiring it can
// never stop a round from closing.
func newUsageReader(configDir string) (usagepkg.Reader, usagepkg.Prices) {
	prices, err := usagepkg.LoadPrices(filepath.Join(configDir, "relay", "prices.json"))
	if err != nil {
		// A bad price file must never stop a round from closing: say so
		// once, on stderr, and run on the embedded default.
		fmt.Fprintf(os.Stderr, "relay: %v (using built-in prices)\n", err)
	}
	reader := usagepkg.New()
	return reader, prices
}
