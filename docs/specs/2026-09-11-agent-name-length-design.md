# Derived agent names are validated before a pane exists

**Issue:** #64

## 1. System overview

herdr requires an agent name to start with a lowercase letter, contain only
lowercase letters, digits, `-` or `_`, and be 1-32 characters. relay's store
enforces the same rule on the **binding** name, but herdr sees the **derived**
agent name: `<name>-builder` (+8) for a builder, `<name>-<role>-<8 hex>`
(+18 for `reviewer`) for a consult. A 25-32 character binding name passes
validation and fails at `bind`; a 15-24 character one binds and fails at
`ask`. Either way the failure comes from `herdr agent start`, after a pane
has been split.

This design validates the derived name at the one place each is built,
before any herdr call, so the failure is a validation error with nothing on
disk and nothing spawned, and the message says what to do. The rule itself
lives in `internal/herdr`, next to the client that will be refused by it,
and the fake herdr enforces it so a fixture cannot drift from the real
client again. `bind`, `add` and `fork` additionally print a one-line note
when the chosen name is short enough to build but too long for a configured
consult role, so the `ask` failure is not a surprise a day later.

Rejected, per the issue: lowering `maxNameLen` (breaks existing bindings on
load and is tight for a sanitised cwd basename) and shortening or
truncating derived names. Truncation is now actively dangerous: since #66
relay identifies agents **by name**, and two long binding names sharing a
prefix would truncate to the same agent name and be mistaken for each other.

### Scope boundary

In scope: `herdr.ValidateAgentName` and `MaxAgentNameLen`; validation in
`resolveBuilder` and `Ask`; the fake's enforcement; `ConsultRolesTooLong`
and the CLI note after `bind`/`add`/`fork`.

Out of scope: the store's binding-name rule (unchanged); `doctor`; any change
to how names are composed.

## 2. File structure

```
internal/herdr/types.go            MaxAgentNameLen, ErrInvalidAgentName, ValidateAgentName
internal/herdr/types_test.go       validation table
internal/relay/bind.go             resolveBuilder validates before builderPane
internal/relay/ask.go              Ask validates before phase 1 (id minted before the lock)
internal/relay/names.go            ConsultRolesTooLong
internal/relay/names_test.go
internal/relay/fake_test.go        StartAgent refuses an invalid name with herdr's message
internal/relay/bind_test.go        long name refused with no pane split
internal/relay/ask_test.go         long name refused with nothing on disk
cmd/relay/main.go                  note after bind/add/fork
```

## 3. Data structures and type definitions

### 3.1 `internal/herdr` (new)

```
const MaxAgentNameLen = 32

var ErrInvalidAgentName = errors.New("invalid agent name")

func ValidateAgentName(name string) error
```

`ValidateAgentName` returns nil for a name herdr accepts, otherwise an error
wrapping `ErrInvalidAgentName` whose text is herdr's own sentence: `agent
name must start with a lowercase letter and contain only lowercase letters,
digits, '-' or '_' (1-32 characters)`, prefixed with the offending name and,
for the length case, its length. The rule is transcribed from herdr 0.9.0's
refusal; the comment says so and that it must follow herdr.

### 3.2 `relay.ConsultRolesTooLong` (new, `names.go`)

```
func ConsultRolesTooLong(t *alias.Table, bindingName string) []string
```

Returns, sorted, the names of consult roles in `t` for which
`bindingName + "-" + role + "-" + <8 hex>` would exceed `MaxAgentNameLen`.
Pure. Empty when every configured consult fits or the table has no
consults. Uses `t.Names()` and `t.Lookup` and `Spec.IsConsult()`.

## 4. Interface definitions and component contracts

### 4.1 `resolveBuilder` (modified)

After `agentName := name + "-builder"` and before `builderPane`:
`herdr.ValidateAgentName(agentName)`; on error return
`fmt.Errorf("builder agent name %q: %w -- use a binding name of at most %d characters", agentName, err, herdr.MaxAgentNameLen-len("-builder"))`.

Postcondition on that error: no `SplitPane`/`CreateTab`, no `StartAgent`.
`Bind`, `Add` and `Fork` all pass through here, so all three are covered;
`Add` and `Fork` create their worktree **before** `resolveBuilder` -- check
whether they do, and if so the validation must run before the worktree is
cut too (a refused name must not leave a worktree behind). The plan says
where.

### 4.2 `Ask` (modified)

Mint `id := newID()` before phase 1, build `agentName` from `opts.Name`,
`spec.Name` and `id`, validate; on error return
`fmt.Errorf("consult agent name %q: %w -- binding %q needs a name of at most %d characters to run %q consults", agentName, err, opts.Name, herdr.MaxAgentNameLen-len("-"+spec.Name+"-")-8, spec.Name)`.

Postcondition: a validation failure writes nothing (no reservation, no
question file). The reservation in phase 1 uses the pre-minted id.

### 4.3 `fakeHerdr.StartAgent` (modified)

Records the call as today, then returns `herdr.ValidateAgentName(name)`'s
error if any (before `startErr`). Every existing fixture name is short, so
no existing test changes.

### 4.4 CLI note

After the success line of `bind` (spawn path only, not adopt/resume),
`add` and `fork`, if `relay.ConsultRolesTooLong(rt.Aliases, name)` is
non-empty print:

```
note: %s is too long for the %s consult role(s); relay ask needs a binding name of at most %d characters for %s
```

with the tightest limit and the role it belongs to. One line, stdout, not
an error.

## 5. High-level pseudocode

```
resolveBuilder:
    agentName := name + "-builder"
    if err := ValidateAgentName(agentName); err != nil: return wrapped   -- before any pane
    …unchanged…

Ask:
    …validation as today…
    id := newID()
    agentName := opts.Name + "-" + spec.Name + "-" + id
    if err := ValidateAgentName(agentName); err != nil: return wrapped   -- before phase 1
    phase 1 uses id and agentName
    …unchanged…

ConsultRolesTooLong:
    for each alias name: spec := Lookup; if !spec.IsConsult(): continue
        if len(bindingName)+1+len(spec.Name)+1+8 > MaxAgentNameLen: collect spec.Name
    sort; return
```

## 6. Error handling strategy

One new error, `herdr.ErrInvalidAgentName`, wrapped by both call sites so
callers can `errors.Is` it. Both are validation failures: nothing on disk,
nothing spawned, recoverable by choosing a shorter name. The CLI note is
not an error and does not change exit status.

## 7. Behavioural rules and their rationale

- **The rule lives in `internal/herdr`.** It is herdr's rule; relay
  transcribes it once, where the client that will be refused lives, and
  both the real call sites and the fake consult the same function.
- **Validate at the composition site, not in the store.** The store's rule
  is about binding names, which are also directory names; the derived
  names are herdr's concern and depend on which suffix is being built.
- **A note, not a refusal, at bind time for consult length.** A binding
  that can build but cannot take a `reviewer` is still a useful binding.
  Refusing it would make the alias table dictate binding names.

## 8. Testing requirements

1. `ValidateAgentName` table: 32 chars ok; 33 refused with the length in
   the message; uppercase start, leading digit, a space, a dot refused;
   empty refused; `errors.Is(err, ErrInvalidAgentName)` for each refusal.
2. `resolveBuilder` via `Bind` with a 25-character name: error wraps
   `ErrInvalidAgentName`, `f.splits == 0`, `len(f.starts) == 0`, no binding
   saved. Same via `Add`: additionally `len(fakeGit.addWorktreeCalls) == 0`.
3. `Ask` with a 15-character binding name and the `reviewer` role: error
   wraps `ErrInvalidAgentName`, `f.splits == 0`, no `Consults` on the
   binding, no question file on disk.
4. `fakeHerdr.StartAgent` returns the herdr-shaped error for a 33-char
   name (pins the fake against drift).
5. `ConsultRolesTooLong`: 14-char name with `reviewer` → empty; 15-char →
   `["reviewer"]`; no consult roles → empty.

Mutation checks: remove the validation in `resolveBuilder` (test 2 must
fail on `f.splits`); remove it in `Ask` (test 3 must fail on the question
file); make the fake accept any name (test 4 must fail).

## 9. Explicitly out of scope

- Changing `maxNameLen` in the store.
- Shortening the derived names.
- A `doctor` check on configured alias names.
