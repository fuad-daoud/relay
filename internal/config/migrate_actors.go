package config

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fuad-daoud/relevo/internal/actors"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// This file is round 2's one-time migration (cockpit spec §3.8; A2 plan round
// 2 R3): it rewrites a pre-actors config -- a roles section, or the legacy
// policy.order/policy.tier and candidate roles/tier fields -- as an actors
// section plus, when a role needs one, an agents section, recorded as one
// revision whose source is "migration". The old keys stop being read after it
// (R4); rolling back re-arms it (a test pins that).

// MigrateToActors migrates the stored config when it has no actors section and
// still carries a roles section, a policy order or tier, or a candidate roles
// or tier key. It reports whether it wrote.
//
// It computes the whole new document first and validates every section before
// opening the transaction, so a failure writes nothing and the old config
// keeps working.
func (s *Store) MigrateToActors() (bool, error) {
	doc, err := s.currentDoc()
	if err != nil {
		return false, err
	}
	if _, ok := doc[Actors]; ok {
		return false, nil
	}
	if !legacyRolesPresent(doc) {
		return false, nil
	}

	set, err := candidateSetFromDoc(doc)
	if err != nil {
		return false, err
	}

	out, notes, err := migrateDoc(doc, set)
	if err != nil {
		return false, err
	}

	for _, sec := range Sections {
		body, ok := out[sec]
		if !ok {
			continue
		}
		if _, err := Validate(sec, body); err != nil {
			return false, err
		}
	}

	message := "roles → actors"
	if len(notes) > 0 {
		message += "; " + strings.Join(notes, "; ")
	}

	if err := s.db.Tx(func(t *db.Tx) error {
		before, err := readSnapshot(t)
		if err != nil {
			return err
		}
		now := s.now().UTC()
		for _, sec := range []Section{Candidates, Agents, Actors, Policy} {
			body, ok := out[sec]
			if !ok {
				continue
			}
			if err := t.ConfigPut(string(sec), body, now); err != nil {
				return err
			}
		}
		if err := t.ConfigDelete(string(Roles)); err != nil {
			return err
		}
		return s.As("migration", message).record(t, before, nil)
	}); err != nil {
		return false, err
	}
	return true, nil
}

// legacyRolesPresent reports whether doc is a pre-actors config: a roles
// section, or an order or tier key in policy, or a roles or tier key on any
// candidate (R3 "When").
func legacyRolesPresent(doc Doc) bool {
	if _, ok := doc[Roles]; ok {
		return true
	}
	if body, ok := doc[Policy]; ok && jsonHasAnyKey(body, "order", "tier") {
		return true
	}
	if body, ok := doc[Candidates]; ok && candidatesHaveAnyKey(body, "roles", "tier") {
		return true
	}
	return false
}

// candidateSetFromDoc parses doc's candidates section, or gives an empty set
// when it is absent, exactly as Load does.
func candidateSetFromDoc(doc Doc) (*candidate.Set, error) {
	body, ok := doc[Candidates]
	if !ok {
		body = []byte("[]")
	}
	set, _, err := candidate.Parse(FileName(Candidates), body)
	if err != nil {
		return nil, err
	}
	return set, nil
}

// ignoredLegacyWarnings names the pre-actors keys Load no longer reads once an
// actors section decides (R4): policy.order, policy.tier, and every candidate
// roles or tier key.
func ignoredLegacyWarnings(candBody []byte, candOK bool, polBody []byte, polOK bool) []string {
	var out []string
	if polOK && jsonHasAnyKey(polBody, "order") {
		out = append(out, "config: policy.order is ignored; actors decide (relevo config log shows the migration)")
	}
	if polOK && jsonHasAnyKey(polBody, "tier") {
		out = append(out, "config: policy.tier is ignored; actors decide (relevo config log shows the migration)")
	}
	if !candOK {
		return out
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(candBody, &rows); err != nil {
		return out
	}
	for i, row := range rows {
		if _, ok := row["roles"]; ok {
			out = append(out, fmt.Sprintf("config: candidates[%d].roles is ignored; actors decide (relevo config log shows the migration)", i))
		}
		if _, ok := row["tier"]; ok {
			out = append(out, fmt.Sprintf("config: candidates[%d].tier is ignored; actors decide (relevo config log shows the migration)", i))
		}
	}
	return out
}

// migrateDoc computes the migrated document from doc and set, with the notes
// the migration revision carries. It is pure: doc is not modified.
func migrateDoc(doc Doc, set *candidate.Set) (Doc, []string, error) {
	pol := policy.Policy{}
	if body, ok := doc[Policy]; ok {
		p, _, err := policy.Parse(FileName(Policy), body)
		if err != nil {
			return nil, nil, err
		}
		pol = p
	}

	var rf *roles.File
	if body, ok := doc[Roles]; ok {
		f, _, err := roles.Parse(FileName(Roles), body)
		if err != nil {
			return nil, nil, err
		}
		rf = f
	}

	reg, err := roles.Build(rf, set, pol)
	if err != nil {
		return nil, nil, err
	}

	// The old rows: the stored roles File in file mode, and -- when the roles
	// section is absent -- the legacy derivation of candidates and policy,
	// which gives every builtin row with its ranked tokens, unlisted ones
	// appended, and its tier (R3 step 1).
	old := rf
	if old == nil {
		f, _, err := roles.FromLegacy(set, pol)
		if err != nil {
			return nil, nil, err
		}
		old = f
	}

	roleTiers := make(map[string]string)
	var notes []string

	// Existing agents survive the migration; a new native agent whose name
	// collides with one of them takes the <role>-agent fallback.
	agentsOut := make(map[string]actors.AgentEntry)
	if body, ok := doc[Agents]; ok {
		existing, _, err := actors.ParseAgents(body)
		if err != nil {
			return nil, nil, err
		}
		for name, entry := range existing {
			agentsOut[name] = entry
		}
	}

	actorsOut := make(map[string]actors.Actor)
	for _, name := range reg.Names() {
		role, _ := reg.Role(name)
		row := old.Rows[name]

		agentName, entry, created := agentForRole(role, agentsOut)
		if agentName == "" {
			// A role with no definition for any kind has nothing to name an
			// agent after and cannot be expressed: skip it rather than write
			// an agents entry that would not validate.
			notes = append(notes, fmt.Sprintf("actor %s: no definitions; skipped", name))
			continue
		}
		if created {
			agentsOut[agentName] = entry
			notes = append(notes, fmt.Sprintf("agent %s: from role %s's definitions", agentName, name))
		}

		a := actors.Actor{Agent: agentName}
		seen := make(map[string]bool)
		for _, raw := range row.Candidates {
			display := raw
			if _, err := set.Resolve(raw); err == nil {
				display = set.NameOf(raw)
			} else {
				notes = append(notes, fmt.Sprintf("actor %s: candidate %q is not configured; kept", name, raw))
			}
			if seen[display] {
				// Two raw entries that resolve to one candidate: the file
				// mode dedupes them on the ranked token, so the actor names
				// the candidate once.
				continue
			}
			seen[display] = true
			a.Candidates = append(a.Candidates, actors.Entry{Candidate: display, Off: containsString(row.Off, raw)})
		}
		if t, ok := reg.RoleTier(name); ok {
			a.Tier = string(t)
			roleTiers[name] = string(t)
		}
		if rf != nil && row.Gate != nil {
			a.Check = row.Gate
		}
		actorsOut[name] = a
	}

	out := make(Doc, len(doc)+2)
	for sec, body := range doc {
		out[sec] = body
	}

	actorsJSON, err := actors.EncodeActors(actorsOut)
	if err != nil {
		return nil, nil, err
	}
	out[Actors] = actorsJSON
	delete(out, Roles)

	if len(agentsOut) > 0 {
		agentsJSON, err := actors.EncodeAgents(agentsOut)
		if err != nil {
			return nil, nil, err
		}
		out[Agents] = agentsJSON
	}

	if body, ok := doc[Policy]; ok {
		updated, err := stripObjectKeys(body, Policy, "order", "tier")
		if err != nil {
			return nil, nil, err
		}
		out[Policy] = updated
	}

	if body, ok := doc[Candidates]; ok {
		updated, candNotes, err := stripCandidateKeys(body, set, roleTiers)
		if err != nil {
			return nil, nil, err
		}
		out[Candidates] = updated
		notes = append(notes, candNotes...)
	}

	return out, notes, nil
}

// agentForRole resolves one role to the agent an actor runs. It returns the
// agent name, the agents entry to add when the agent is new, and whether that
// entry has to be added. An empty name means the role has no definition at
// all and cannot be expressed as an actor.
func agentForRole(role roles.Role, agentsOut map[string]actors.AgentEntry) (string, actors.AgentEntry, bool) {
	if shipped, ok := shippedAgentFor(role); ok {
		return shipped, actors.AgentEntry{}, false
	}
	if len(role.Definitions) == 0 {
		return "", actors.AgentEntry{}, false
	}

	name := nativeAgentName(role)
	if _, shipped := actors.Shipped(name); shipped {
		name = role.Name + "-agent"
	}
	if _, exists := agentsOut[name]; exists {
		name = role.Name + "-agent"
	}

	native := make(map[string]roles.DefRow, len(role.Definitions))
	for kind, d := range role.Definitions {
		native[kind] = roles.DefRow{
			Agent:    d.Agent,
			Requires: append([]string(nil), d.Requires...),
		}
	}
	entry := actors.AgentEntry{Shape: shapeWord(role.Shape), Native: native}
	return name, entry, true
}

// shippedAgentFor returns the shipped agent name for a builtin role whose
// definitions are, for every kind in harness.All(), exactly the shipped ones
// (Agent and Requires). A role with no such match, or with a kind missing, is
// not shipped.
func shippedAgentFor(role roles.Role) (string, bool) {
	spec, ok := harness.RoleByName(role.Name)
	if !ok {
		return "", false
	}
	var wants []string
	if len(spec.Definitions) > 1 {
		wants = spec.Definitions[1:]
	}
	for _, h := range harness.All() {
		d, ok := role.Definitions[h.Kind]
		if !ok {
			return "", false
		}
		if d.Agent != spec.Definition || !sameStrings(d.Requires, wants) {
			return "", false
		}
	}
	return spec.Definition, true
}

// nativeAgentName names a role's native agent after its claude definition's
// agent when every kind uses the same agent and requires, and <role>-agent
// otherwise (R3 step 2).
func nativeAgentName(role roles.Role) string {
	claude, ok := role.Definitions["claude"]
	if !ok {
		return role.Name + "-agent"
	}
	for kind, d := range role.Definitions {
		if kind == "claude" {
			continue
		}
		if d.Agent != claude.Agent || !sameStrings(d.Requires, claude.Requires) {
			return role.Name + "-agent"
		}
	}
	return claude.Agent
}

// shapeWord renders a role shape as the actors section's word.
func shapeWord(shape harness.RoleShape) string {
	if shape == harness.ShapeBuilder {
		return "writer"
	}
	return "reader"
}

// sameStrings reports whether a and b hold the same strings in order.
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// containsString reports whether list holds want.
func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// jsonHasAnyKey reports whether body is a JSON object carrying any of keys.
func jsonHasAnyKey(body []byte, keys ...string) bool {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return false
	}
	for _, k := range keys {
		if _, ok := obj[k]; ok {
			return true
		}
	}
	return false
}

// candidatesHaveAnyKey reports whether any element of body's JSON array
// carries one of keys.
func candidatesHaveAnyKey(body []byte, keys ...string) bool {
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(body, &rows); err != nil {
		return false
	}
	for _, row := range rows {
		for _, k := range keys {
			if _, ok := row[k]; ok {
				return true
			}
		}
	}
	return false
}

// stripObjectKeys removes keys from a JSON object body, keeping every other
// key and its bytes, and returns the re-indented object.
func stripObjectKeys(body []byte, sec Section, keys ...string) ([]byte, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, fmt.Errorf("%s: %v", sec, err)
	}
	for _, k := range keys {
		delete(obj, k)
	}
	return encodeSection(obj)
}

// stripCandidateKeys removes each element's roles and tier keys, keeping
// `name` and every other key, and notes a candidate tier an actor cannot carry
// (R3 step 3).
func stripCandidateKeys(body []byte, set *candidate.Set, roleTiers map[string]string) ([]byte, []string, error) {
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, nil, fmt.Errorf("%s: %v", Candidates, err)
	}

	var notes []string
	for _, row := range rows {
		tier := rawString(row["tier"])
		rolesOf := rawStrings(row["roles"])
		delete(row, "roles")
		delete(row, "tier")

		if tier == "" || !tierDropped(tier, rolesOf, roleTiers) {
			continue
		}
		notes = append(notes, fmt.Sprintf("candidate %s: tier %s dropped; set it on the actor", candidateLabel(row, set), tier))
	}

	encoded, err := encodeSection(rows)
	if err != nil {
		return nil, nil, err
	}
	return encoded, notes, nil
}

// tierDropped reports whether a candidate tier has no actor to live on: it is
// dropped when no role the candidate lists carries that same tier.
func tierDropped(tier string, rolesOf []string, roleTiers map[string]string) bool {
	if len(rolesOf) == 0 {
		return true
	}
	for _, r := range rolesOf {
		if roleTiers[r] == tier {
			return false
		}
	}
	return true
}

// candidateLabel names a candidate element for a note: its name key when set,
// else the harness/provider/model ref it carries, else "?".
func candidateLabel(row map[string]json.RawMessage, set *candidate.Set) string {
	if name := rawString(row["name"]); name != "" {
		return name
	}
	ref := strings.Join([]string{
		rawString(row["harness"]),
		rawString(row["provider"]),
		rawString(row["model"]),
	}, "/")
	if set != nil {
		if c, err := set.Resolve(ref); err == nil {
			return c.Name
		}
	}
	if ref == "//" {
		return "?"
	}
	return ref
}

// rawString returns the JSON string in raw, or "" when raw is absent or not a
// string.
func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// rawStrings returns the JSON array of strings in raw, or nil.
func rawStrings(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// encodeSection renders a section body as indented JSON with a trailing
// newline, the shape the store's other writes use.
func encodeSection(v any) ([]byte, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
