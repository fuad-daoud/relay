package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// This file is the revision half of the store (docs/specs/
// 2026-09-24-cockpit-design.md §6.4): Store labelling, the in-transaction
// recorder every write runs, and the rollback/log readers. The writing half of
// each verb stays in config.go and import.go, which call record as the last
// statement of their transactions.

// ErrNoRevision reports a rollback or a lookup naming a revision that is not
// stored.
var ErrNoRevision = errors.New("no such revision")

// ErrNoChange reports a rollback whose target equals the stored config, so
// there is nothing to write.
var ErrNoChange = errors.New("config already equals that revision")

// As returns a copy of s labelled with source and message. The receiver is not
// modified, so a runtime's base store stays unlabelled for its other callers.
func (s *Store) As(source, message string) *Store {
	c := *s
	c.source = source
	c.message = message
	return &c
}

// WithClock returns a copy of s whose writes and revisions read the time from
// now. Tests pin a fixed clock with it; ImportFiles uses it to reuse the time
// it was already given.
func (s *Store) WithClock(now func() time.Time) *Store {
	c := *s
	c.now = now
	return &c
}

// readDoc reads every stored section, in Sections order. It is the document
// form the diff and the snapshot use: secrets are never part of it.
func readDoc(t *db.Tx) (Doc, error) {
	doc := Doc{}
	for _, sec := range Sections {
		body, ok, err := t.ConfigGet(string(sec))
		if err != nil {
			return nil, err
		}
		if ok {
			doc[sec] = body
		}
	}
	return doc, nil
}

// snapshot is a document together with the config version it was read at. A
// baseline records the version the config had before the write, which a bare
// Doc cannot carry.
type snapshot struct {
	doc     Doc
	version int64
}

// readSnapshot reads the stored document and the config version, both at the
// start of the same Tx.
func readSnapshot(t *db.Tx) (snapshot, error) {
	doc, err := readDoc(t)
	if err != nil {
		return snapshot{}, err
	}
	version, err := t.ConfigVersion()
	if err != nil {
		return snapshot{}, err
	}
	return snapshot{doc: doc, version: version}, nil
}

// record appends one revision for a write that changed something. It is the
// last statement of every write transaction, so a failure here aborts the
// write with it: the config change and its revision commit together or not at
// all (§4.2 step 6).
//
// before is the document and config version read at the start of the same Tx.
// extra carries changes that never appear in a document: secret puts and
// deletes.
func (s *Store) record(t *db.Tx, before snapshot, extra []Change) error {
	after, err := readDoc(t)
	if err != nil {
		return err
	}

	changes := append(DiffDocs(before.doc, after), extra...)
	if len(changes) == 0 {
		return nil
	}

	version, err := t.ConfigVersion()
	if err != nil {
		return err
	}

	// The first revision ever written on a machine that already has config is
	// preceded by a baseline, so the pre-revision state can be rolled back to.
	// The baseline carries the version the config had before the write.
	if len(before.doc) > 0 {
		exists, err := t.RevisionsExist()
		if err != nil {
			return err
		}
		if !exists {
			snapshot, err := EncodeDoc(before.doc)
			if err != nil {
				return err
			}
			if _, err := t.RevisionInsert(db.RevisionRow{
				At:       s.now(),
				Source:   "baseline",
				Message:  "config before revisions",
				Version:  before.version,
				Changes:  []byte("[]"),
				Snapshot: snapshot,
			}); err != nil {
				return err
			}
		}
	}

	source := s.source
	if source == "" {
		source = "unknown"
	}
	changesJSON, err := json.Marshal(changes)
	if err != nil {
		return err
	}
	snapshot, err := EncodeDoc(after)
	if err != nil {
		return err
	}
	if _, err := t.RevisionInsert(db.RevisionRow{
		At:       s.now(),
		Source:   source,
		Message:  s.message,
		Version:  version,
		Changes:  changesJSON,
		Snapshot: snapshot,
	}); err != nil {
		return err
	}
	return nil
}

// Log returns revisions newest first; limit <= 0 returns them all.
func (s *Store) Log(limit int) ([]db.RevisionRow, error) { return s.db.Revisions(limit) }

// Revision returns one revision with its snapshot, ErrNoRevision when it is
// absent.
func (s *Store) Revision(rev int64) (db.RevisionRow, error) {
	r, ok, err := s.db.Revision(rev)
	if err != nil {
		return db.RevisionRow{}, err
	}
	if !ok {
		return db.RevisionRow{}, fmt.Errorf("%w: #%d", ErrNoRevision, rev)
	}
	return r, nil
}

// RollbackPlan returns the changes rolling back to rev would make (current to
// rev's snapshot). When there are none it returns ErrNoChange, so the CLI can
// answer "already equals" without opening a transaction.
func (s *Store) RollbackPlan(rev int64) ([]Change, error) {
	target, err := s.Revision(rev)
	if err != nil {
		return nil, err
	}
	snapshot, err := decodeSnapshot(target.Snapshot)
	if err != nil {
		return nil, err
	}
	current, err := s.currentDoc()
	if err != nil {
		return nil, err
	}
	changes := DiffDocs(current, snapshot)
	if len(changes) == 0 {
		return nil, ErrNoChange
	}
	return changes, nil
}

// Rollback makes the stored config equal to rev's snapshot, as one new
// revision with source "rollback" and message "rollback to #<rev>", plus
// "; "+s.message when a message was given. Secrets are not rolled back.
//
// A snapshot that no longer validates (a later relevo tightened a rule) is
// returned as "rollback to #N: <error>" with nothing written. When every
// section already matches, it returns ErrNoChange and writes nothing.
func (s *Store) Rollback(rev int64) (db.RevisionRow, error) {
	target, err := s.Revision(rev)
	if err != nil {
		return db.RevisionRow{}, err
	}
	snapshot, err := decodeSnapshot(target.Snapshot)
	if err != nil {
		return db.RevisionRow{}, err
	}
	for _, sec := range Sections {
		body, ok := snapshot[sec]
		if !ok {
			continue
		}
		if _, err := Validate(sec, body); err != nil {
			return db.RevisionRow{}, fmt.Errorf("rollback to #%d: %w", rev, err)
		}
	}

	message := fmt.Sprintf("rollback to #%d", rev)
	if s.message != "" {
		message += "; " + s.message
	}
	s = s.As("rollback", message)

	noChange := false
	if err := s.db.Tx(func(t *db.Tx) error {
		before, err := readSnapshot(t)
		if err != nil {
			return err
		}
		if len(DiffDocs(before.doc, snapshot)) == 0 {
			noChange = true
			return nil
		}

		now := s.now().UTC()
		for _, sec := range Sections {
			body, ok := snapshot[sec]
			if !ok {
				continue
			}
			if current, present := before.doc[sec]; present && len(diffRaw(string(sec), current, body)) == 0 {
				continue
			}
			if err := t.ConfigPut(string(sec), body, now); err != nil {
				return err
			}
		}
		for _, sec := range Sections {
			if _, ok := snapshot[sec]; ok {
				continue
			}
			if _, present := before.doc[sec]; !present {
				continue
			}
			if err := t.ConfigDelete(string(sec)); err != nil {
				return err
			}
		}
		return s.record(t, before, nil)
	}); err != nil {
		return db.RevisionRow{}, err
	}
	if noChange {
		return db.RevisionRow{}, ErrNoChange
	}

	rows, err := s.db.Revisions(1)
	if err != nil {
		return db.RevisionRow{}, err
	}
	if len(rows) == 0 {
		return db.RevisionRow{}, errors.New("rollback recorded no revision")
	}
	return rows[0], nil
}

// currentDoc reads the stored document through the *DB readers, for callers
// with no transaction open.
func (s *Store) currentDoc() (Doc, error) {
	doc := Doc{}
	for _, sec := range Sections {
		body, ok, err := s.db.ConfigGet(string(sec))
		if err != nil {
			return nil, err
		}
		if ok {
			doc[sec] = body
		}
	}
	return doc, nil
}

// decodeSnapshot parses a revision's stored snapshot object into a document.
func decodeSnapshot(raw []byte) (Doc, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("revision snapshot: %w", err)
	}
	doc := Doc{}
	for name, body := range m {
		doc[Section(name)] = body
	}
	return doc, nil
}
