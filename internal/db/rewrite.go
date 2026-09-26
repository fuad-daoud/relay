package db

import (
	"context"
	"fmt"
	"strings"
)

// Prefix is one absolute-directory substitution; the matcher is in RewritePathPrefix.
type Prefix struct{ Old, New string }

// RewritePathPrefix rewrites old-root paths in every TEXT column of every user
// table, for every pair. A newer-schema database is left untouched (newer ==
// true). Otherwise every update runs in one transaction and rows counts them.
func RewritePathPrefix(ctx context.Context, path string, pairs []Prefix) (rows int64, newer bool, err error) {
	if err := ctx.Err(); err != nil {
		return 0, false, err
	}

	d, err := Open(path)
	if err != nil {
		return 0, false, err
	}
	defer func() {
		if cerr := d.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	if d.Newer() {
		return 0, true, nil
	}

	err = d.Tx(func(tx *Tx) error {
		var terr error
		rows, terr = rewriteTouching(tx, pairs)
		return terr
	})
	if err != nil {
		// The transaction rolled back, so nothing changed and no row count is
		// honest.
		return 0, false, err
	}
	return rows, false, nil
}

// rewriteTouching runs every pair against every TEXT column of every table.
func rewriteTouching(tx *Tx, pairs []Prefix) (int64, error) {
	tables, err := userTables(tx)
	if err != nil {
		return 0, err
	}
	var rows int64
	for _, table := range tables {
		cols, err := textColumns(tx, table)
		if err != nil {
			return 0, err
		}
		for _, col := range cols {
			n, err := rewriteColumn(tx, table, col, pairs)
			if err != nil {
				return 0, err
			}
			rows += n
		}
	}
	return rows, nil
}

func rewriteColumn(tx *Tx, table, col string, pairs []Prefix) (int64, error) {
	var rows int64
	for _, p := range pairs {
		res, err := tx.exec(rewritePrefixSQL(table, col),
			p.New, len(p.Old)+1, p.Old, len(p.Old)+1, p.Old+"/")
		if err != nil {
			return 0, fmt.Errorf("rewrite %s.%s: %w", table, col, mapBusy(err))
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("rewrite %s.%s: rows affected: %w", table, col, err)
		}
		rows += n
	}
	return rows, nil
}

// rewritePrefixSQL is the one statement every (table, column, pair) runs. It
// uses substr, not LIKE, so % and _ in the old root stay literal; identifiers
// are quoted, so a keyword-named table or column still works.
func rewritePrefixSQL(table, col string) string {
	q := quoteIdent(col)
	return fmt.Sprintf(
		`UPDATE %s SET %s = ? || substr(%s, ?) WHERE %s = ? OR substr(%s, 1, ?) = ?`,
		quoteIdent(table), q, q, q, q)
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func userTables(tx *Tx) ([]string, error) {
	rows, err := tx.query(
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name <> 'schema_version' ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", mapBusy(err))
	}
	names, err := collectRows(rows, scanString)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	return names, nil
}

// textColumn is one column and whether its declared type is TEXT.
type textColumn struct {
	name string
	text bool
}

func textColumns(tx *Tx, table string) ([]string, error) {
	rows, err := tx.query("PRAGMA table_info(" + quoteIdent(table) + ")")
	if err != nil {
		return nil, fmt.Errorf("table_info %s: %w", table, mapBusy(err))
	}
	cols, err := collectRows(rows, scanTextColumn)
	if err != nil {
		return nil, fmt.Errorf("table_info %s: %w", table, err)
	}
	var names []string
	for _, c := range cols {
		if c.text {
			names = append(names, c.name)
		}
	}
	return names, nil
}

func scanTextColumn(s rowScanner) (textColumn, error) {
	var (
		cid       int
		name      string
		ctype     string
		notNull   int
		dfltValue any
		pk        int
	)
	if err := s.Scan(&cid, &name, &ctype, &notNull, &dfltValue, &pk); err != nil {
		return textColumn{}, err
	}
	return textColumn{name: name, text: strings.EqualFold(strings.TrimSpace(ctype), "TEXT")}, nil
}
