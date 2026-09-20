package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fuad-daoud/relay/internal/db"
)

// openDB ensures path's directory exists (Open's precondition) and opens
// it, migrating as needed.
func openDB(path string) (*db.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	return db.Open(path)
}

// cmdDB dispatches `relay db path|migrate|stats`. It never opens the
// database through the shared runtime constructor: only the subcommands
// here do, since nothing else in this round reads it.
func cmdDB(args []string) error {
	const usage = `usage: relay db path
       relay db migrate
       relay db stats`

	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return exitCodeErr{code: 2}
	}

	switch args[0] {
	case "path":
		return cmdDBPath(args[1:])
	case "migrate":
		return cmdDBMigrate(args[1:])
	case "stats":
		return cmdDBStats(args[1:])
	case "help", "-h", "--help":
		fmt.Println(usage)
		return nil
	default:
		fmt.Fprintf(os.Stderr, "relay db: unknown subcommand %q\n", args[0])
		fmt.Fprintln(os.Stderr, usage)
		return exitCodeErr{code: 2}
	}
}

func cmdDBPath(_ []string) error {
	rt, err := newRuntime()
	if err != nil {
		return err
	}

	fmt.Println(rt.Store.DBPath())
	return nil
}

func cmdDBMigrate(_ []string) error {
	rt, err := newRuntime()
	if err != nil {
		return err
	}
	path := rt.Store.DBPath()

	d, err := openDB(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "relay db: %s: %v\n", path, err)
		return exitCodeErr{code: 1}
	}
	defer d.Close()

	v, err := d.Version()
	if err != nil {
		fmt.Fprintf(os.Stderr, "relay db: %s: %v\n", path, err)
		return exitCodeErr{code: 1}
	}

	fmt.Printf("relay.db at %s: schema version %d\n", path, v)
	return nil
}

func cmdDBStats(_ []string) error {
	rt, err := newRuntime()
	if err != nil {
		return err
	}
	path := rt.Store.DBPath()

	d, err := openDB(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "relay db: %s: %v\n", path, err)
		return exitCodeErr{code: 1}
	}
	defer d.Close()

	stats, err := d.Stats()
	if err != nil {
		fmt.Fprintf(os.Stderr, "relay db: %s: %v\n", path, err)
		return exitCodeErr{code: 1}
	}

	fmt.Print(formatStats(stats))
	return nil
}

// formatStats renders Stats as `relay db stats` prints it: one line per
// table in alphabetical order, then a summary line. It is a pure function of
// its argument so tests never need a database.
func formatStats(s db.Stats) string {
	var out strings.Builder

	names := make([]string, 0, len(s.Rows))
	for name := range s.Rows {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(&out, "%s  %d\n", name, s.Rows[name])
	}

	newest := "-"
	if s.NewestRound != nil {
		newest = s.NewestRound.UTC().Format(time.RFC3339)
	}
	fmt.Fprintf(&out, "size %s  version %d  newest round %s\n", humanBytes(s.SizeBytes), s.Version, newest)

	return out.String()
}

// humanBytes renders n as a binary (1024-based) human-readable size, e.g.
// "512 B", "1.5 KiB", "3.0 MiB".
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
