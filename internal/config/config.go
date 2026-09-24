// Package config is the one home of relevo's configuration and secrets. They
// live in the machine database (schema v2's config_doc and secret tables), and
// the files under ~/.config/relevo are imported into it once and removed
// (docs/specs/2026-09-24-db-as-record-design.md §2, §4.6).
//
// This package imports candidate, policy, roles, usage, remote/client and db,
// and none of them imports internal/relevo or this package, so the store stays
// a leaf the runtime can hold.
package config

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/usage"
)

// Section names one config_doc row. Hooks is stored as a JSON map of event
// type to argv lists rather than a file.
type Section string

const (
	Candidates Section = "candidates"
	Policy     Section = "policy"
	Roles      Section = "roles"
	Prices     Section = "prices"
	Servers    Section = "servers"
	Hooks      Section = "hooks"
)

// Sections lists every section in the order an import checks and stores them.
var Sections = []Section{Candidates, Policy, Roles, Prices, Servers, Hooks}

// sectionFile maps a section to the file it is imported from. Hooks has none:
// it is a directory of argv lists.
var sectionFile = map[Section]string{
	Candidates: "candidates.json",
	Policy:     "policy.json",
	Roles:      "roles.json",
	Prices:     "prices.json",
	Servers:    "servers.json",
}

// FileName returns the on-disk file a section is imported from, or "" for
// Hooks.
func FileName(sec Section) string { return sectionFile[sec] }

// Secret names, and the files they are imported from.
const (
	// SecretClientKey stores the PEM bytes remote.MarshalPrivate writes.
	SecretClientKey = "client.key"
	// SecretTypesafe stores the trimmed TypeSafe API key bytes.
	SecretTypesafe = "typesafe"
)

const (
	clientKeyFile   = "client.key"
	typesafeKeyFile = "typesafe.key"
	clientPubFile   = "client.pub"
	aliasesFile     = "aliases.json"
)

// Files returns the file names a config dir may hold and ImportFiles consumes.
// Hooks is a directory and is not in the list.
func Files() []string {
	files := make([]string, 0, len(Sections)+2)
	for _, sec := range Sections {
		if name := FileName(sec); name != "" {
			files = append(files, name)
		}
	}
	return append(files, clientKeyFile, typesafeKeyFile)
}

// Hooks maps an event type to the argv lists run for it, in order.
type HooksMap map[string][][]string

// Loaded is one consistent read of every section and both secrets.
//
// An absent section reproduces the corresponding missing file exactly: an
// absent candidates or servers section is an empty, non-nil collection, an
// absent policy is the zero Policy, an absent roles section keeps the legacy
// derivation, an absent prices section is the embedded default, and an absent
// hooks section is an empty map.
type Loaded struct {
	Candidates *candidate.Set
	Policy     policy.Policy
	RolesFile  *roles.File
	Registry   *roles.Registry
	Prices     usage.Prices
	Servers    client.Servers
	Hooks      HooksMap
	ClientKey  []byte
	Typesafe   string
	Warnings   []string
	Version    int64
}

// Store reads and writes the config sections and secrets of one database. Its
// source, message and clock label every write's revision; Open gives a store
// its default clock, and As/WithClock copy it with a label or a test clock.
type Store struct {
	db      *db.DB
	source  string
	message string
	now     func() time.Time
}

// Open returns a Store over d.
func Open(d *db.DB) *Store { return &Store{db: d, now: time.Now} }

// Load reads every section and both secrets. A stored body that does not parse
// is returned as an error: it cannot happen after a validated Put.
func (s *Store) Load() (Loaded, error) {
	var L Loaded

	if body, ok, err := s.db.ConfigGet(string(Candidates)); err != nil {
		return Loaded{}, err
	} else if ok {
		set, warnings, err := candidate.Parse(FileName(Candidates), body)
		if err != nil {
			return Loaded{}, err
		}
		L.Candidates = set
		L.Warnings = append(L.Warnings, warnings...)
	} else {
		// An absent section is today's missing file: an empty set. Parse of an
		// empty array is exactly candidate.Load's missing-file result.
		set, _, err := candidate.Parse(FileName(Candidates), []byte("[]"))
		if err != nil {
			return Loaded{}, err
		}
		L.Candidates = set
	}

	if body, ok, err := s.db.ConfigGet(string(Policy)); err != nil {
		return Loaded{}, err
	} else if ok {
		pol, warnings, err := policy.Parse(FileName(Policy), body)
		if err != nil {
			return Loaded{}, err
		}
		L.Policy = pol
		L.Warnings = append(L.Warnings, warnings...)
	}

	if body, ok, err := s.db.ConfigGet(string(Roles)); err != nil {
		return Loaded{}, err
	} else if ok {
		f, warnings, err := roles.Parse(FileName(Roles), body)
		if err != nil {
			return Loaded{}, err
		}
		L.RolesFile = f
		L.Warnings = append(L.Warnings, warnings...)
	}

	if body, ok, err := s.db.ConfigGet(string(Prices)); err != nil {
		return Loaded{}, err
	} else if ok {
		p, err := usage.ParsePrices(body)
		if err != nil {
			return Loaded{}, err
		}
		L.Prices = p
	} else {
		L.Prices = usage.DefaultPrices()
	}

	if body, ok, err := s.db.ConfigGet(string(Servers)); err != nil {
		return Loaded{}, err
	} else if ok {
		srv, err := client.ParseServers(body)
		if err != nil {
			return Loaded{}, err
		}
		L.Servers = srv
	} else {
		L.Servers = client.Servers{}
	}

	L.Hooks = HooksMap{}
	if body, ok, err := s.db.ConfigGet(string(Hooks)); err != nil {
		return Loaded{}, err
	} else if ok {
		if err := json.Unmarshal(body, &L.Hooks); err != nil {
			return Loaded{}, fmt.Errorf("%s: %v", Hooks, err)
		}
		if L.Hooks == nil {
			L.Hooks = HooksMap{}
		}
	}

	if key, ok, err := s.db.SecretGet(SecretClientKey); err != nil {
		return Loaded{}, err
	} else if ok {
		L.ClientKey = key
	}

	if ts, ok, err := s.db.SecretGet(SecretTypesafe); err != nil {
		return Loaded{}, err
	} else if ok {
		L.Typesafe = string(ts)
	}

	reg, err := roles.Build(L.RolesFile, L.Candidates, L.Policy)
	if err != nil {
		return Loaded{}, err
	}
	L.Registry = reg

	v, err := s.db.ConfigVersion()
	if err != nil {
		return Loaded{}, err
	}
	L.Version = v

	return L, nil
}

// Body returns the stored JSON body of sec, and ok false when it is absent.
func (s *Store) Body(sec Section) ([]byte, bool, error) {
	return s.db.ConfigGet(string(sec))
}

// Has reports whether the section has a stored body.
func (s *Store) Has(sec Section) (bool, error) {
	_, ok, err := s.Body(sec)
	return ok, err
}

// Secret returns name's stored value, and ok false when it is absent.
// PutSecret is its write side.
func (s *Store) Secret(name string) ([]byte, bool, error) {
	return s.db.SecretGet(name)
}

// Version returns config_meta.version, the counter Refresh compares against.
func (s *Store) Version() (int64, error) { return s.db.ConfigVersion() }

// Validate runs the §4.3 parser for sec over body, returning its warnings. A
// section it does not know is an error.
func Validate(sec Section, body []byte) ([]string, error) {
	switch sec {
	case Candidates:
		_, warnings, err := candidate.Parse(FileName(sec), body)
		return warnings, err
	case Policy:
		_, warnings, err := policy.Parse(FileName(sec), body)
		return warnings, err
	case Roles:
		_, warnings, err := roles.Parse(FileName(sec), body)
		return warnings, err
	case Prices:
		_, err := usage.ParsePrices(body)
		return nil, err
	case Servers:
		_, err := client.ParseServers(body)
		return nil, err
	case Hooks:
		var h HooksMap
		if err := json.Unmarshal(body, &h); err != nil {
			return nil, fmt.Errorf("%s: %v", Hooks, err)
		}
		return nil, nil
	default:
		return nil, fmt.Errorf("unknown config section %q", sec)
	}
}

// Put validates body and, when it is valid, stores it as sec in one
// transaction. A refused body writes nothing.
func (s *Store) Put(sec Section, body []byte) ([]string, error) {
	if sec == Candidates {
		filled, _, err := fillCandidateNames(body)
		if err != nil {
			return nil, err
		}
		body = filled
	}
	warnings, err := Validate(sec, body)
	if err != nil {
		return nil, err
	}
	if err := s.db.Tx(func(t *db.Tx) error {
		before, err := readSnapshot(t)
		if err != nil {
			return err
		}
		if err := t.ConfigPut(string(sec), body, s.now().UTC()); err != nil {
			return err
		}
		return s.record(t, before, nil)
	}); err != nil {
		return nil, err
	}
	return warnings, nil
}

// Delete removes sec's stored body, if any, in one transaction. The version
// bumps even when sec was absent: it is a change counter for readers, not a
// count of stored sections.
func (s *Store) Delete(sec Section) error {
	return s.db.Tx(func(t *db.Tx) error {
		before, err := readSnapshot(t)
		if err != nil {
			return err
		}
		if err := t.ConfigDelete(string(sec)); err != nil {
			return err
		}
		return s.record(t, before, nil)
	})
}

// PutDoc stores every section in doc. It checks every name and validates every
// body first: an unknown section or the first invalid body aborts with nothing
// written. The store's ConfigPuts then run in one transaction, so the version
// bumps once per section in the document rather than once per call. Sections
// not named in doc are untouched.
func (s *Store) PutDoc(doc map[Section]json.RawMessage) ([]string, error) {
	var unknown []string
	for sec := range doc {
		if !known(sec) {
			unknown = append(unknown, string(sec))
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("unknown config section %q", unknown[0])
	}

	if body, ok := doc[Candidates]; ok {
		filled, _, err := fillCandidateNames(body)
		if err != nil {
			return nil, err
		}
		doc[Candidates] = filled
	}

	var warnings []string
	for _, sec := range Sections {
		body, ok := doc[sec]
		if !ok {
			continue
		}
		w, err := Validate(sec, body)
		if err != nil {
			return nil, err
		}
		warnings = append(warnings, w...)
	}

	now := s.now().UTC()
	if err := s.db.Tx(func(t *db.Tx) error {
		before, err := readSnapshot(t)
		if err != nil {
			return err
		}
		for _, sec := range Sections {
			body, ok := doc[sec]
			if !ok {
				continue
			}
			if err := t.ConfigPut(string(sec), body, now); err != nil {
				return err
			}
		}
		return s.record(t, before, nil)
	}); err != nil {
		return nil, err
	}

	return warnings, nil
}

// known reports whether sec names a stored section.
func known(sec Section) bool {
	for _, s := range Sections {
		if s == sec {
			return true
		}
	}
	return false
}

// PutSecret stores value under name. The client key is validated with
// remote.ParsePrivate first, so a malformed PEM never reaches the database.
func (s *Store) PutSecret(name string, value []byte) error {
	if name == SecretClientKey {
		if _, err := remote.ParsePrivate(value); err != nil {
			return err
		}
	}
	return s.db.Tx(func(t *db.Tx) error {
		before, err := readSnapshot(t)
		if err != nil {
			return err
		}
		if err := t.SecretPut(name, value, s.now().UTC()); err != nil {
			return err
		}
		return s.record(t, before, []Change{{Path: "secret." + name, Op: "set"}})
	})
}

// SecretDelete removes name's stored value, if any. A name that was not stored
// writes nothing: there is no change to record.
func (s *Store) SecretDelete(name string) error {
	return s.db.Tx(func(t *db.Tx) error {
		before, err := readSnapshot(t)
		if err != nil {
			return err
		}
		if _, ok, err := t.SecretGet(name); err != nil {
			return err
		} else if !ok {
			return nil
		}
		if err := t.SecretDelete(name); err != nil {
			return err
		}
		return s.record(t, before, []Change{{Path: "secret." + name, Op: "remove"}})
	})
}

// SecretNames returns every stored secret's name, sorted. It never returns a
// value.
func (s *Store) SecretNames() ([]string, error) { return s.db.SecretNames() }
