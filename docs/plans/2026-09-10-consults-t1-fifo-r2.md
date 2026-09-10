# Consults Task 1, round 2: make the index test actually pin the index

Round 1 was correct and is committed (`5114f9e`). `make check` passes and the
scope was exactly the four declared files. This round fixes a defect in **the
plan you were given**, not in your work, plus the three stale comments you
flagged.

## Why round 2 exists

I mutation-tested your commit: I made `confirmIndex` ignore its `idx` argument
and confirm the newest unconfirmed entry instead.

`TestPendingForPlannerReturnsArrivalOrder` failed, correctly.
**`TestConfirmIndexConfirmsOnlyTheNamedEntry` passed.**

It passed because it confirms index 2 of three entries -- and index 2 is *also*
the newest unconfirmed entry, so "confirm the index I was given" and "confirm
the newest" produce identical results. The test cannot tell the two apart, so
it pins nothing. That is the exact failure mode `CLAUDE.md` warns about: a test
that passes with and without the logic is not pinning anything.

## Task

- [ ] **Step 1: Strengthen the test**

In `internal/store/log_test.go`, replace `TestConfirmIndexConfirmsOnlyTheNamedEntry`
entirely with this. The change is which index is confirmed (1, the *older*
pending entry, not 2) and correspondingly which assertions run.

```go
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

	// Confirm the OLDER of the two pending entries. Naming index 2 would pin
	// nothing: index 2 is also the newest unconfirmed entry, so an
	// implementation that ignored idx and confirmed the newest would pass.
	if err := s.ConfirmIndex(name, 1); err != nil {
		t.Fatalf("ConfirmIndex: %v", err)
	}

	entries, err := s.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	if !entries[1].Confirmed {
		t.Error("index 1 not confirmed; ConfirmIndex must confirm the index it was given")
	}
	if entries[2].Confirmed {
		t.Error("index 2 confirmed; ConfirmIndex must confirm ONLY the index it was given, never the newest")
	}
	if entries[1].DeliveredAt == nil {
		t.Error("DeliveredAt not stamped on the confirmed entry")
	}
}
```

- [ ] **Step 2: Prove the new test pins, by breaking the implementation**

Temporarily edit `internal/store/confirmIndex` in `internal/store/log.go`, adding
this immediately after the range check, so `idx` is ignored:

```go
	// TEMPORARY MUTATION -- revert before committing
	for j := len(entries) - 1; j >= 0; j-- {
		if entries[j].Direction == DirToPlanner && !entries[j].Confirmed {
			idx = j
			break
		}
	}
```

Run:

```bash
dev run go test ./internal/store/ -run TestConfirmIndexConfirmsOnlyTheNamedEntry -v
```

Expected: **FAIL**, reporting that index 2 was confirmed.

If it PASSES, stop and report -- the test still is not pinning and there is no
point continuing.

- [ ] **Step 3: Revert the mutation**

```bash
git checkout internal/store/log.go
```

Confirm `git status --porcelain` shows only `internal/store/log_test.go` modified.
Then re-run the test and expect PASS.

- [ ] **Step 4: Fix the three stale comments you flagged**

All three are now in scope. Change only the wording; touch no logic.

`internal/store/log.go`, the `readLog` doc comment (you reported it at `:159`):
replace the phrase `pendingForPlanner and confirmLatest` with
`pendingForPlanner and confirmIndex`.

`internal/relay/deliver.go`, the `DeliverPending` doc comment (you reported it
at `:43`): replace `attempts the newest pending payload` with
`attempts the oldest pending payload`.

`internal/relay/pull.go`, the `Pull` doc comment (you reported it at `:9`):
replace `returns the newest pending payload` with
`returns the oldest pending payload`.

Check for any other occurrence of the words "newest pending" or "confirmLatest"
in the tree and fix those too:

```bash
grep -rn "confirmLatest\|ConfirmLatest\|newest pending\|newest undelivered" --include='*.go' .
```

Anything that grep still finds after your edits, report -- do not guess at it.

- [ ] **Step 5: Verify**

```bash
dev run make check
```

Expected: PASS. `make` is intercepted on this laptop; `dev run` is how it runs.

- [ ] **Step 6: Commit**

```bash
git add internal/store/log.go internal/store/log_test.go internal/relay/deliver.go internal/relay/pull.go
git commit -m "test(store): pin ConfirmIndex to the index it was given

The test confirmed index 2 of three entries, which is also the newest
unconfirmed entry, so an implementation that ignored idx and confirmed the
newest passed it unchanged. Confirming the older pending entry instead tells
the two apart.

Also corrects three doc comments left describing the newest-first scan."
```

## Constraints

- Do not change `pendingForPlanner` or `confirmIndex` logic. Round 1's
  implementation is correct and verified; this round changes a test and four
  comments.
- Do not touch any other task's files.
- If a step is impossible as written, stop and say so in your report.
