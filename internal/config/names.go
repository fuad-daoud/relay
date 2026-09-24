package config

import (
	"encoding/json"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/candidate"
)

// EnsureCandidateNames is A1's migration (cockpit spec §3.8): it gives every
// stored candidate a `name`, deriving the ones that lack a name exactly as
// candidate.Parse does in memory. It is the written half of a rule that holds
// without it -- Parse derives names anyway -- so a failure is only a warning
// for the caller.
//
// It is idempotent: once every element has a non-empty "name" it returns
// false and writes nothing. It keeps every key and the element order, because
// it edits the decoded JSON objects rather than re-encoding candidate.Candidate.
// The written body is indented the way `config export` prints, and Put is one
// transaction, so a concurrent CLI and daemon compute the same names.
func (s *Store) EnsureCandidateNames() (bool, error) {
	body, ok, err := s.Body(Candidates)
	if err != nil || !ok {
		return false, err
	}

	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(body, &rows); err != nil {
		return false, fmt.Errorf("%s: %v", FileName(Candidates), err)
	}

	missing := false
	for _, row := range rows {
		name, err := rowName(row)
		if err != nil {
			return false, fmt.Errorf("%s: %v", FileName(Candidates), err)
		}
		if name == "" {
			missing = true
			break
		}
	}
	if !missing {
		return false, nil
	}

	var entries []candidate.Candidate
	if err := json.Unmarshal(body, &entries); err != nil {
		return false, fmt.Errorf("%s: %v", FileName(Candidates), err)
	}
	names := candidate.DeriveNames(entries)
	if len(names) != len(rows) {
		return false, fmt.Errorf("%s: %d entries, %d names", FileName(Candidates), len(rows), len(names))
	}

	for i, row := range rows {
		name, err := rowName(row)
		if err != nil {
			return false, fmt.Errorf("%s: %v", FileName(Candidates), err)
		}
		if name != "" {
			continue
		}
		raw, err := json.Marshal(names[i])
		if err != nil {
			return false, err
		}
		row["name"] = raw
	}

	encoded, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return false, fmt.Errorf("%s: %v", FileName(Candidates), err)
	}
	encoded = append(encoded, '\n')

	if _, err := s.Put(Candidates, encoded); err != nil {
		return false, err
	}
	return true, nil
}

// rowName returns the "name" a decoded candidate object carries, or "" when it
// has none.
func rowName(row map[string]json.RawMessage) (string, error) {
	raw, ok := row["name"]
	if !ok {
		return "", nil
	}
	var name string
	if err := json.Unmarshal(raw, &name); err != nil {
		return "", err
	}
	return name, nil
}
