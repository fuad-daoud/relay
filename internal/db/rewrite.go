package db

import (
	"context"
	"fmt"
	"strings"
)

// Prefix is one absolute-directory substitution: every TEXT value that is Old
// exactly, or starts with Old followed by "/", becomes New plus the same tail.
// internal/migrate keeps its own Prefix; the CLI adapts one to the other.
type Prefix struct{ Old, New string }

// RewritePathPrefix rewrites old-root paths stored in the database at path:
// every TEXT column of every user table, for every pair.
//
// A database whose schema is newer than this binary is left untouched and
// returned with newer == true, following the rule Open enforces (#372).
// Otherwise every update runs in one transaction, so a failure leaves the
// database exactly as it was, and rows reports how many rows changed.
//
// The comparison uses substr, not LIKE: the old root is a literal, and LIKE
// would treat % and _ in it as wildcards. Matching is case-exact and anchored
// at the start of the value, so /old/rootother is never confused with
// /old/root.
func RewritePathPrefix(ctx context.Context, path string, pairs []Prefix) (rows int64, newer bool, err error) {
	if err := ctx.Err(); err != nil {
		return 0, false, err
	}

	d, err := Open(path)
	if err != nil {
		return 0, false, err
	}
	defer d.Close()

	if d.Newer() {
		return 0, true, nil
	}

	err = d.Tx(func(tx *Tx) error {
		tables, err := userTables(tx)
		if err != nil {
			return err
		}
		for _, table := range tables {
			cols, err := textColumns(tx, table)
			if err != nil {
				return err
			}
			for _, col := range cols {
				for _, p := range pairs {
					res, err := tx.exec(rewritePrefixSQL(table, col),
						p.New, len(p.Old)+1, p.Old, len(p.Old)+1, p.Old+"/")
					if err != nil {
						return fmt.Errorf("rewrite %s.%s: %w", table, col, mapBusy(err))
					}
					n, err := res.RowsAffected()
					if err != nil {
						return fmt.Errorf("rewrite %s.%s: rows affected: %w", table, col, err)
					}
					rows += n
				}
			}
		}
		return nil
	})
	if err != nil {
		// The transaction rolled back, so nothing changed and no row count is
		// honest.
		return 0, false, err
	}
	return rows, false, nil
}

// rewritePrefixSQL is the one statement every (table, column, pair) runs: the
// new prefix is prepended to the tail after Old, but only for a value that is
// Old exactly or starts with Old + "/". Identifiers are quoted, so a table or
// column named with a keyword still works.
func rewritePrefixSQL(table, col string) string {
	q := quoteIdent(col)
	return fmt.Sprintf(
		`UPDATE %s SET %s = ? || substr(%s, ?) WHERE %s = ? OR substr(%s, 1, ?) = ?`,
		quoteIdent(table), q, q, q, q)
}

// quoteIdent quotes one sqlite identifier with double quotes, doubling any
// internal double quote.
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// userTables lists the database's own tables, in a stable order: sqlite's
// internal tables and schema_version are not user data.
func userTables(tx *Tx) ([]string, error) {
	rows, err := tx.query(
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name <> 'schema_version' ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", mapBusy(err))
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan table name: %w", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	return names, nil
}

// textColumns lists the columns of table whose declared type is TEXT
// (case-insensitive), which is the only place a path can be stored.
func textColumns(tx *Tx, table string) ([]string, error) {
	rows, err := tx.query("PRAGMA table_info(" + quoteIdent(table) + ")")
	if err != nil {
		return nil, fmt.Errorf("table_info %s: %w", table, mapBusy(err))
	}
	defer rows.Close()

	var cols []string
	for rows.Next() {
		var (
			cid       int
			name      string
			ctype     string
			notNull   int
			dfltValue any
			pk        int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dfltValue, &pk); err != nil {
			return nil, fmt.Errorf("scan table_info %s: %w", table, err)
		}
		if strings.EqualFold(strings.TrimSpace(ctype), "TEXT") {
			cols = append(cols, name)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("table_info %s: %w", table, err)
	}
	return cols, nil
}
