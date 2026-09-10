# Consults Task 1: FIFO pending queue

> Executing **one task** from a larger plan. Do not implement any other task,
> and do not "helpfully" add the neighbouring ones -- another builder is
> working them concurrently in its own worktree and your edits would race.

**Full plan:** `docs/plans/2026-09-10-consults.md` (this is Task 1 of 8)
**Design spec:** `docs/specs/2026-09-10-consults-design.md`
**Issue:** #36

Both are in your worktree. Read the spec section a step cites when a step's
rationale is not obvious -- the plan tells you what, the spec tells you why.

## Stop rather than improvise

If a step is impossible as written, or the plan contradicts what you find in
the code, **stop and say so in your report**. Do not bend a test to fit, and do
not invent an API that is not in the plan. A halt that surfaces a design error
is worth more than a green suite that worked around one.

## Global Constraints

- Verification is `make check`, never `go test ./...` alone. It adds `gofmt -l .` over the whole tree, `go vet`, and a `go mod tidy` check.
- Go stdlib only. Do not add a module dependency; `go mod tidy` must produce no diff.
- Compose relay config paths through `userConfigRoot()` (`cmd/relay/main.go`), never by hand.
- relay makes no judgements. The only gate on a consult is `does the findings file exist`.
- "Read-only" is a property of the role's configuration, never a claim relay enforces. Do not write a comment or a doc line saying relay prevents writes.
- Every new exported symbol gets a doc comment saying what it does and, where a choice was made, why. Match the existing house style: comments explain rationale, not mechanics.
- Existing `bind.json` files must load, round-trip and re-serialise unchanged until a consult exists on them. Both new `Binding` fields carry `omitempty`.
- Commit when the task's steps are all done. Do not push, and do not open a PR.

- You are already in your own git worktree on your own branch. Do not create
  another branch and do not switch branches.

---

### Task 1: FIFO pending queue

The queue is LIFO today: `pendingForPlanner` scans backwards for the newest unconfirmed planner-bound entry and `confirmLatest` confirms the newest. Consults make several simultaneously-pending payloads normal, and under LIFO a round report queued first is delivered last, behind findings that arrived after it. This task flips the scan and replaces the implicit "both functions happen to pick the same entry" coupling with an explicit index.

Only `Tx.PendingForPlanner` gains the index. `Store.PendingForPlanner` keeps its three-value signature because its callers (`status.go:147` and ~30 tests) only read.

**Files:**
- Modify: `internal/store/log.go:16` (comment), `:77-93` (Store wrappers), `:108-118` (Tx methods), `:196-215` (`pendingForPlanner`), `:217-247` (`confirmLatest`)
- Modify: `internal/relay/deliver.go:66`, `:93`
- Modify: `internal/relay/pull.go:22`, `:29`
- Test: `internal/store/log_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `func (t *Tx) PendingForPlanner(name string) (LogEntry, int, bool, error)` — entry, its index in the log, found, error
  - `func (t *Tx) ConfirmIndex(name string, idx int) error`
  - `func (s *Store) ConfirmIndex(name string, idx int) error`
  - `func (s *Store) PendingForPlanner(name string) (LogEntry, bool, error)` — **unchanged signature**
  - `Tx.ConfirmLatest` and `Store.ConfirmLatest` are **removed**.

- [ ] **Step 1: Write the failing tests**

Replace `TestPendingForPlannerFindsNewestUnconfirmed` (`internal/store/log_test.go:45`), `TestConfirmLatestClearsPending` (`:68`) and `TestConfirmLatestOnlyConfirmsNewestUnconfirmed` (`:102`) with these four. Delete all three old tests — the first two assert the behaviour being reversed.

```go
func TestPendingForPlannerReturnsArrivalOrder(t *testing.T) {
	s, name := seedBinding(t)

	// A report, a blocked-dialog question and a drift note can already be
	// pending together today; consults only make it routine. Arrival order is
	// the only order relay can defend without judging content.
	for _, e := range []LogEntry{
		{Round: 3, Direction: DirToPlanner, Kind: KindReport, Payload: "report r3"},
		{Round: 3, Direction: DirToPlanner, Kind: KindQuestion, Payload: "question r3"},
		{Round: 3, Direction: DirToPlanner, Kind: KindDrift, Payload: "drift r3"},
	} {
		if err := s.AppendLog(name, e); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}

	want := []string{"report r3", "question r3", "drift r3"}
	for i, w := range want {
		var got LogEntry
		var idx int
		var ok bool
		err := s.WithLock(func(tx *Tx) error {
			var err error
			got, idx, ok, err = tx.PendingForPlanner(name)
			return err
		})
		if err != nil || !ok {
			t.Fatalf("delivery %d: PendingForPlanner ok=%v err=%v", i, ok, err)
		}
		if got.Payload != w {
			t.Fatalf("delivery %d = %q, want %q", i, got.Payload, w)
		}
		if err := s.ConfirmIndex(name, idx); err != nil {
			t.Fatalf("ConfirmIndex: %v", err)
		}
	}

	if _, found, err := s.PendingForPlanner(name); err != nil || found {
		t.Fatalf("queue not drained: found=%v err=%v", found, err)
	}
}

func TestConfirmIndexConfirmsOnlyTheNamedEntry(t *testing.T) {
	s, name := seedBinding(t)

	for _, e := range []LogEntry{
		{Round: 1, Direction: DirToBuilder, Kind: KindPlan, Confirmed: true},
		{Round: 1, Direction: DirToPlanner, Kind: KindReport, Payload: "first"},
		{Round: 1, Direction: DirToPlanner, Kind: KindReport, Payload: "second"},
	} {
		if err := s.AppendLog(name, e); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}

	if err := s.ConfirmIndex(name, 2); err != nil {
		t.Fatalf("ConfirmIndex: %v", err)
	}

	entries, err := s.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if entries[1].Confirmed {
		t.Error("index 1 confirmed; ConfirmIndex must confirm only the index it was given")
	}
	if !entries[2].Confirmed {
		t.Error("index 2 not confirmed")
	}
	if entries[2].DeliveredAt == nil {
		t.Error("DeliveredAt not stamped on the confirmed entry")
	}
}

func TestConfirmIndexClearsPending(t *testing.T) {
	s, name := seedBinding(t)
	if err := s.AppendLog(name, LogEntry{Round: 1, Direction: DirToPlanner, Kind: KindReport, Payload: "x"}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	if err := s.ConfirmIndex(name, 0); err != nil {
		t.Fatalf("ConfirmIndex: %v", err)
	}

	if _, found, err := s.PendingForPlanner(name); err != nil || found {
		t.Fatalf("still pending after confirm: found=%v err=%v", found, err)
	}
}

func TestConfirmIndexRejectsAnIndexOutsideTheLog(t *testing.T) {
	s, name := seedBinding(t)
	if err := s.AppendLog(name, LogEntry{Round: 1, Direction: DirToPlanner, Kind: KindReport, Payload: "x"}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	if err := s.ConfirmIndex(name, 7); err == nil {
		t.Fatal("ConfirmIndex(7) on a 1-entry log returned nil; an out-of-range index is a caller bug, not a no-op")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/store/ -run 'TestPendingForPlannerReturnsArrivalOrder|TestConfirmIndex' -v
```

Expected: FAIL to compile — `tx.PendingForPlanner` returns 3 values, not 4; `s.ConfirmIndex` undefined.

- [ ] **Step 3: Flip the scan and add the index**

In `internal/store/log.go`, replace `pendingForPlanner` (`:196`) and `confirmLatest` (`:217`):

```go
// pendingForPlanner returns the OLDEST undelivered payload bound for the
// planner and its index in the log.
//
// Oldest-first because arrival order is the only order relay can defend
// without judging content. Under the newest-first scan this replaced, a round
// report queued before two consult findings was delivered after both of them.
func (s *Store) pendingForPlanner(name string) (LogEntry, int, bool, error) {
	entries, err := s.readLog(name)
	if err != nil {
		return LogEntry{}, 0, false, err
	}

	for i, e := range entries {
		if e.Direction == DirToPlanner && !e.Confirmed {
			return e, i, true, nil
		}
	}

	return LogEntry{}, 0, false, nil
}

// confirmIndex marks one entry as delivered by rewriting the log. The log is
// small and append-only, so a full rewrite is simpler and safer than in-place
// mutation.
//
// It takes an index rather than re-deriving "the entry we must have meant"
// because the pair it replaced -- pendingForPlanner and confirmLatest -- agreed
// only by both scanning for the newest unconfirmed entry. That coupling was
// implicit and survived exactly as long as nobody edited one of them. Callers
// hold the state lock across both calls, so the index is stable.
func (s *Store) confirmIndex(name string, idx int) error {
	entries, err := s.readLog(name)
	if err != nil {
		return err
	}
	if idx < 0 || idx >= len(entries) {
		return fmt.Errorf("confirm entry %d for %q: log has %d entries", idx, name, len(entries))
	}
	if entries[idx].Confirmed {
		return nil
	}

	now := time.Now().UTC()
	entries[idx].Confirmed = true
	entries[idx].DeliveredAt = &now

	var buf []byte
	for _, e := range entries {
		raw, err := json.Marshal(e)
		if err != nil {
			return fmt.Errorf("encode log entry: %w", err)
		}
		buf = append(buf, raw...)
		buf = append(buf, '\n')
	}

	return writeFileAtomic(s.logPath(name), buf, bindingFileMode)
}
```

- [ ] **Step 4: Update the Store and Tx wrappers**

Replace `internal/store/log.go:77-93` and `:108-118`:

```go
// PendingForPlanner returns the oldest undelivered payload bound for the
// planner, acquiring the state lock for the operation. It drops the entry's
// index: a caller that only reads cannot confirm, and a caller that intends to
// confirm must hold the lock across both calls and so must go through Tx.
func (s *Store) PendingForPlanner(name string) (LogEntry, bool, error) {
	var e LogEntry
	var found bool
	err := s.WithLock(func(tx *Tx) error {
		var err error
		e, _, found, err = tx.PendingForPlanner(name)
		return err
	})
	return e, found, err
}

// ConfirmIndex marks the entry at idx as delivered, acquiring the state lock
// for the operation.
func (s *Store) ConfirmIndex(name string, idx int) error {
	return s.WithLock(func(tx *Tx) error { return tx.ConfirmIndex(name, idx) })
}

// PendingForPlanner reads the oldest undelivered planner payload and its index
// under the held lock.
func (t *Tx) PendingForPlanner(name string) (LogEntry, int, bool, error) {
	return t.s.pendingForPlanner(name)
}

// ConfirmIndex rewrites the log under the held lock, marking the entry at idx
// delivered.
func (t *Tx) ConfirmIndex(name string, idx int) error {
	return t.s.confirmIndex(name, idx)
}
```

Update the `maxLogEntries` comment at `:16` to name `ConfirmIndex` instead of `ConfirmLatest`.

- [ ] **Step 5: Update the two confirming callers**

`internal/relay/deliver.go:66` and `:93`:

```go
	pending, idx, found, err := tx.PendingForPlanner(b.Name)
	if err != nil {
		return Delivery{}, err
	}
	if !found {
		return Delivery{Empty: true, Reason: "nothing pending"}, nil
	}
```

```go
	if err := tx.ConfirmIndex(b.Name, idx); err != nil {
		return Delivery{}, err
	}
```

`internal/relay/pull.go:22` and `:29`:

```go
		pending, idx, ok, err := tx.PendingForPlanner(name)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if err := tx.ConfirmIndex(name, idx); err != nil {
			return err
		}
```

- [ ] **Step 6: Run the full suite**

```bash
make check
```

Expected: PASS. If a `reconcile_test.go` case fails, read it before changing it — with one pending payload FIFO and LIFO are identical, so a failure there means two payloads were pending and the test encoded the old order.

- [ ] **Step 7: Commit**

```bash
git add internal/store/log.go internal/store/log_test.go internal/relay/deliver.go internal/relay/pull.go
git commit -m "fix(store): deliver pending planner payloads in arrival order

pendingForPlanner scanned backwards for the newest unconfirmed entry and
confirmLatest confirmed the newest, so a round report queued before two other
payloads was delivered after both. Consults make several simultaneously
pending payloads routine, which turns an ordering quirk into a real one.

The pair agreed only by both scanning for the same end of the log. Confirming
by index makes the caller name the entry it claimed."
```
