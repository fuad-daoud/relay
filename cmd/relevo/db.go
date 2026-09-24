package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/ingest"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// openDB ensures path's directory exists (Open's precondition) and opens
// it, migrating as needed.
func openDB(path string) (*db.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	return db.Open(path)
}

// cmdDB dispatches `relevo db path|migrate|stats`. It never opens the
// database through the shared runtime constructor: only the subcommands
// here do, since nothing else in this round reads it.
func cmdDB(args []string) error {
	const usage = `usage: relevo db path
       relevo db migrate
       relevo db stats
       relevo db backfill [--dry-run] [--archive-only|--live-only]`

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
	case "backfill":
		return cmdDBBackfill(args[1:])
	case "help", "-h", "--help":
		fmt.Println(usage)
		return nil
	default:
		fmt.Fprintf(os.Stderr, "relevo db: unknown subcommand %q\n", args[0])
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
		fmt.Fprintf(os.Stderr, "relevo db: %s: %v\n", path, err)
		return exitCodeErr{code: 1}
	}
	defer d.Close()

	// A schema a newer relevo wrote is never migrated by this binary: refuse
	// with the version pair so the operator upgrades instead (#372 §4.5).
	if err := d.CheckMigrate(); err != nil {
		fmt.Fprintf(os.Stderr, "relevo db: %s: %v\n", path, err)
		return exitCodeErr{code: 1}
	}

	v, err := d.Version()
	if err != nil {
		fmt.Fprintf(os.Stderr, "relevo db: %s: %v\n", path, err)
		return exitCodeErr{code: 1}
	}

	fmt.Printf("relevo.db at %s: schema version %d\n", path, v)
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
		fmt.Fprintf(os.Stderr, "relevo db: %s: %v\n", path, err)
		return exitCodeErr{code: 1}
	}
	defer d.Close()

	stats, err := d.Stats()
	if err != nil {
		fmt.Fprintf(os.Stderr, "relevo db: %s: %v\n", path, err)
		return exitCodeErr{code: 1}
	}

	fmt.Print(formatStats(stats))
	return nil
}

// cmdDBBackfill ingests every archived tarball (oldest first) and every
// live binding into the database, once, printing one line per source. It
// is idempotent -- internal/ingest's cursors make a second run a no-op --
// and the recovery path if the db is ever deleted
// (docs/specs/2026-09-20-persistence-design.md §5.6).
func cmdDBBackfill(args []string) error {
	const usage = `usage: relevo db backfill [--dry-run] [--archive-only|--live-only]`

	fs := flag.NewFlagSet("backfill", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "open sources and count what would be ingested, writing nothing")
	archiveOnly := fs.Bool("archive-only", false, "ingest only archived (gc'd) bindings")
	liveOnly := fs.Bool("live-only", false, "ingest only live bindings")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *archiveOnly && *liveOnly {
		fmt.Fprintln(os.Stderr, "relevo db backfill: --archive-only and --live-only are mutually exclusive")
		fmt.Fprintln(os.Stderr, usage)
		return exitCodeErr{code: 2}
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	// --dry-run ingests into a scratch database instead of the real one, so
	// the counts it prints are real Ingest output without writing anything
	// the operator's machine keeps.
	targetPath := rt.Store.DBPath()
	prefix := ""
	if *dryRun {
		scratchDir, serr := os.MkdirTemp("", "relevo-db-backfill-dryrun-*")
		if serr != nil {
			return fmt.Errorf("relevo db backfill: dry-run scratch dir: %w", serr)
		}
		defer os.RemoveAll(scratchDir)
		targetPath = filepath.Join(scratchDir, "relevo.db")
		prefix = "would "
	}

	d, err := openDB(targetPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "relevo db: %s: %v\n", targetPath, err)
		return exitCodeErr{code: 1}
	}
	defer d.Close()

	deps := relevo.IngestDeps(rt)
	ctx := context.Background()
	failed := false

	if !*liveOnly {
		archives, aerr := rt.Store.ListArchives()
		if aerr != nil {
			return fmt.Errorf("relevo db backfill: list archives: %w", aerr)
		}
		for _, a := range archives {
			src, serr := ingest.TarSource(a.Path)
			if serr != nil {
				fmt.Print(formatBackfillFailure(prefix, a.Name, serr))
				failed = true
				continue
			}
			stats, ierr := ingest.Ingest(ctx, src, d, deps)
			if ierr != nil {
				fmt.Print(formatBackfillFailure(prefix, a.Name, ierr))
				failed = true
				continue
			}
			fmt.Print(formatBackfillLine(prefix, a.Name, a.At.UTC().Format(time.RFC3339), stats))
		}
	}

	if !*archiveOnly {
		bindings, lerr := rt.Store.List()
		if lerr != nil {
			return fmt.Errorf("relevo db backfill: list bindings: %w", lerr)
		}
		for _, b := range bindings {
			src := ingest.StoreSource(rt.Store, b.Name)
			stats, ierr := ingest.Ingest(ctx, src, d, deps)
			if ierr != nil {
				fmt.Print(formatBackfillFailure(prefix, b.Name, ierr))
				failed = true
				continue
			}
			fmt.Print(formatBackfillLine(prefix, b.Name, "-", stats))
		}
	}

	if failed {
		return exitCodeErr{code: 1}
	}
	return nil
}

// formatBackfillLine is one source's backfill result line: "<name>  <stamp>
// rounds N events N artifacts N transcript N", "-" for a live source's
// stamp (it has no archive time), prefixed "would " under --dry-run.
func formatBackfillLine(prefix, name, stamp string, stats ingest.Stats) string {
	return fmt.Sprintf("%s%s  %s  rounds %d events %d artifacts %d transcript %d\n",
		prefix, name, stamp, stats.Rounds, stats.Events, stats.Artifacts, stats.TranscriptRecords)
}

// formatBackfillFailure is one source's backfill failure line: "<name>
// FAILED: <err>", prefixed "would " under --dry-run.
func formatBackfillFailure(prefix, name string, err error) string {
	return fmt.Sprintf("%s%s  FAILED: %v\n", prefix, name, err)
}

// formatStats renders Stats as `relevo db stats` prints it: one line per
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
	fmt.Fprintf(&out, "size %s  version %d  newest round %s\n", relevo.HumanBytes(s.SizeBytes), s.Version, newest)

	return out.String()
}
