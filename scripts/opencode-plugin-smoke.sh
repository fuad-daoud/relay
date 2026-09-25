#!/usr/bin/env bash
# Smoke driver for relevo OpenCode plugin (#393)
# See docs/plans/2026-09-24-opencode-tui-b1-plugin.md §5.2
#
# SC2329 (shellcheck >= 0.10) and SC2317 (older, e.g. CI's Ubuntu runner):
# `cleanup` runs from `trap ... EXIT`, and each `check_assertion_*` helper is
# invoked indirectly, as an argument to `assert`.
# shellcheck disable=SC2317,SC2329
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d /tmp/relevo-smoke-XXXXXX)"
CONFIG="$TMP/config"
OUT="${OUT:-$TMP/out}"
STATE="$TMP/state"
LOG="$TMP/fake.log"
SESSION_TMUX="relevo-plugin-smoke"

mkdir -p "$CONFIG/plugins/relevo" "$OUT" "$STATE"
touch "$LOG"

cleanup() {
  tmux kill-session -t "$SESSION_TMUX" 2>/dev/null || true
  rm -rf "$TMP"
}
trap cleanup EXIT

# 1. Copy (not symlink) internal/harness/opencodeplugin/ to $CONFIG/plugins/relevo/
cp "$REPO/internal/harness/opencodeplugin/package.json" "$CONFIG/plugins/relevo/"
cp "$REPO/internal/harness/opencodeplugin/tui.tsx" "$CONFIG/plugins/relevo/"
cp "$REPO/internal/harness/opencodeplugin/server.ts" "$CONFIG/plugins/relevo/"

# Config isolation
ln -sf "$HOME/.config/opencode/opencode.jsonc" "$CONFIG/opencode.jsonc"
cat > "$CONFIG/cli.json" <<'EOF'
{
  "$schema": "https://opencode.ai/v2/cli.json",
  "session": { "sidebar": "auto", "scrollbar": false, "thinking": "hide" },
  "animations": false
}
EOF

# Find newest top-level session
SESSION_ID="$(sqlite3 -readonly "$HOME/.local/share/opencode/opencode.db" \
  "select id from session where parent_id is null order by time_updated desc limit 1;" 2>/dev/null || true)"

if [ -z "$SESSION_ID" ]; then
  echo "Error: no top-level OpenCode session found" >&2
  exit 1
fi

send() { tmux send-keys -t "$SESSION_TMUX" "$@"; }
type_lit() { tmux send-keys -t "$SESSION_TMUX" -l "$1"; }

capture() {
  local name="$1"
  tmux capture-pane -t "$SESSION_TMUX" -p > "$OUT/$name.txt" 2>/dev/null || : > "$OUT/$name.txt"
  tmux capture-pane -t "$SESSION_TMUX" -p -e > "$OUT/$name.ansi" 2>/dev/null || : > "$OUT/$name.ansi"
}

wait_ready() {
  # The TUI draws a "Loading plugins..." splash while plugins resolve. Wait for
  # that splash to clear *and* for the session view to have real content: an
  # empty pane (before the first draw) also lacks "Loading plugins", so the
  # splash test alone can return early and capture nothing.
  local i=0
  while [ "$i" -lt "${1:-60}" ]; do
    pane="$(tmux capture-pane -t "$SESSION_TMUX" -p 2>/dev/null || true)"
    if ! printf '%s' "$pane" | grep -q "Loading plugins"; then
      if [ "$(printf '%s\n' "$pane" | grep -c '[^[:space:]]')" -ge 5 ]; then
        return 0
      fi
    fi
    sleep 0.5
    i=$((i + 1))
  done
  echo "wait_ready: TUI did not settle after ${1:-60} polls" >&2
  return 1
}

tmux kill-session -t "$SESSION_TMUX" 2>/dev/null || true

# Launch standalone opencode in tmux
tmux new-session -d -s "$SESSION_TMUX" -x 160 -y 45 \
  "env PATH=$REPO/scripts/testdata/opencode-plugin:$PATH \
       OPENCODE_CONFIG_DIR=$CONFIG \
       RELEVO_FAKE_LOG=$LOG \
       RELEVO_FAKE_STATE=$STATE \
       XDG_STATE_HOME=$STATE \
       opencode --standalone --print-logs -s $SESSION_ID 2> $TMP/opencode.log"

wait_ready 60

# Let initial registration and 1st poll complete
sleep 3
capture "01-session"

# Wait ≥ 12 s after launch to ensure 3rd poll has fired (count >= 3 -> status-2.json)
sleep 11
capture "02-after-toast"

# 03-fleet (C-x o)
send C-x; sleep 0.4; send o; sleep 1.5
capture "03-fleet"

# 04-binding: enter on webshop. Rule 1 opens the report -- webshop needs you
# and has report_round 3 -- on that round, with no key pressed.
send Enter; sleep 1.5
capture "04-binding"

# 04b-landing: escape back to the session, reopen the fleet, down to landing
# (no report -> the transcript tab), enter.
send Escape; sleep 0.5
send C-x; sleep 0.4; send o; sleep 1.0
send Down; sleep 0.3; send Down; sleep 0.3; send Enter; sleep 1.5
capture "04b-landing"

# 05-binding-report: back to the fleet, up to webshop, enter -> the report tab.
send Escape; sleep 0.5
send C-x; sleep 0.4; send o; sleep 1.0
send Up; sleep 0.3; send Up; sleep 0.3; send Enter; sleep 1.5
capture "05-binding-report"

# 05b/05c: two PageDowns, then two polls (>= 12 s) with the body scrolled; the
# first body line must be unchanged and must not be line 01.
send PageDown; sleep 0.4; send PageDown; sleep 0.8
capture "05b-scrolled"
sleep 12
capture "05c-after-polls"

# 06-dialog (a)
send a; sleep 1.5
capture "06-dialog"

# 07-dialog-actions (step into the option list), 08-confirm-done (down to
# Mark done, enter), then enter on confirm
send Down; sleep 1.5
capture "07-dialog-actions"
send Down; sleep 0.3; send Down; sleep 0.3; send Enter; sleep 1.5
capture "08-confirm-done"
send Enter; sleep 1.5
capture "09-after-done"

# 10-palette (ctrl+p, type relevo)
send Escape; sleep 0.5
send C-p; sleep 1.0; send C-u; type_lit "relevo"; sleep 1.5
capture "10-palette"
send Escape; sleep 0.5

tmux kill-session -t "$SESSION_TMUX" 2>/dev/null || true

# Assertions
FAILED=0
assert() {
  local num="$1"
  local desc="$2"
  shift 2
  if "$@"; then
    echo "assertion $num: PASS -- $desc"
  else
    echo "assertion $num: FAIL -- $desc"
    FAILED=1
  fi
}

echo "=== Running Assertions ==="

# 1. log has planner init --kind opencode --session <that session id>
assert 1 "log has planner init --kind opencode --session $SESSION_ID" \
  grep -q "planner init --kind opencode --session $SESSION_ID" "$LOG"

# 2. 01-session shows relevo · oc-smoke, webshop, NEEDS YOU
check_assertion_2() {
  grep -q "relevo · oc-smoke" "$OUT/01-session.txt" && \
  grep -q "webshop" "$OUT/01-session.txt" && \
  grep -q "NEEDS YOU" "$OUT/01-session.txt"
}
assert 2 "01-session shows relevo · oc-smoke, webshop, NEEDS YOU" check_assertion_2

# 3. 01-session shows relevo 1 need you
assert 3 "01-session shows relevo 1 need you" \
  grep -q "relevo 1 need you" "$OUT/01-session.txt"

# 4. 02-after-toast shows ledger r … report in, delivered to chat (the toast) and relevo 2 need you
check_assertion_4() {
  grep -qE "ledger r.*report in, delivered to chat" "$OUT/02-after-toast.txt" && \
  grep -q "relevo 2 need you" "$OUT/02-after-toast.txt"
}
assert 4 "02-after-toast shows ledger report toast and relevo 2 need you" check_assertion_4

# 5. 03-fleet shows relevo › fleet and the header NAME … TOKENS
check_assertion_5() {
  grep -q "relevo › fleet" "$OUT/03-fleet.txt" && \
  grep -q "NAME" "$OUT/03-fleet.txt" && \
  grep -q "TOKENS" "$OUT/03-fleet.txt"
}
assert 5 "03-fleet shows relevo › fleet and column header" check_assertion_5

# 6. 04-binding shows relevo › fleet › webshop and the tab row
check_assertion_6() {
  grep -q "relevo › fleet › webshop" "$OUT/04-binding.txt" && \
  grep -q "plan" "$OUT/04-binding.txt" && \
  grep -q "report" "$OUT/04-binding.txt"
}
assert 6 "04-binding shows relevo › fleet › webshop and tab row" check_assertion_6

# 7. log has show webshop … --report after step 05, and 05-binding-report
#    draws the report body the fake served
check_assertion_7() {
  grep -qE "show webshop.*--report" "$LOG" && \
  grep -q "checkout flow implementation is ready" "$OUT/05-binding-report.txt"
}
assert 7 "log has show webshop … --report and 05-binding-report draws it" check_assertion_7

# 8. 06-dialog shows tell the planner (or, with the fallback, Tell the planner…)
assert 8 "06-dialog shows tell the planner" \
  grep -qi "tell the planner" "$OUT/06-dialog.txt"

# 9. log has done webshop after step 08
assert 9 "log has done webshop after step 08" \
  grep -q "done webshop" "$LOG"

# 10. 10-palette lists Open relevo
assert 10 "10-palette lists Open relevo" \
  grep -q "Open relevo" "$OUT/10-palette.txt"

# 11. no line in the log starts with anything but a relevo verb (sanity)
check_assertion_11() {
  local bad_lines
  bad_lines="$(grep -vE "^(planner|status|history|show|send|stop|done|gate)\b" "$LOG" | grep -v "^$" || true)"
  [ -z "$bad_lines" ]
}
assert 11 "no line in fake log starts with anything but a relevo verb" check_assertion_11

# 12. 03-fleet shows a recent row below ── recent. history.json lists webshop
#     r4 twice (a builder switch), webshop r3, ledger r2, landing r1, so
#     ledger r2 is one of the rows shown.
check_assertion_12() {
  awk '/── recent/{after = 1; next} after' "$OUT/03-fleet.txt" | grep -qE "ledger +r2"
}
assert 12 "03-fleet shows the third recent row (ledger r2)" check_assertion_12

# 13. the §2 item 3 grep prints nothing. -r because that grep names a
#     directory, which plain grep refuses to descend into.
check_assertion_13() {
  ! grep -rn -E "oc-smoke|pl_smoke|webshop|ledger|landing" \
    "$REPO/internal/harness/opencodeplugin/" >/dev/null 2>&1
}
assert 13 "no fixture names in internal/harness/opencodeplugin/" check_assertion_13

# 14. in 03-fleet the NEEDS YOU of the first two rows starts at one column.
#     LC_ALL makes awk count characters, so the › marker counts as one column.
check_assertion_14() {
  local c1 c2
  c1="$(LC_ALL=C.UTF-8 awk '/NEEDS YOU/{print index($0, "NEEDS YOU"); exit}' "$OUT/03-fleet.txt")"
  c2="$(LC_ALL=C.UTF-8 awk '/NEEDS YOU/{n++; if (n == 2) {print index($0, "NEEDS YOU"); exit}}' "$OUT/03-fleet.txt")"
  [ -n "$c1" ] && [ "$c1" = "$c2" ]
}
assert 14 "03-fleet first two rows share the NEEDS YOU column" check_assertion_14

# 15. webshop's status-1 row is display ACTIVE but needs_you true with
#     report_round 3 and round 4: the sidebar must follow needs_you (NEEDS YOU)
#     and the report's round (r3), not the display word or the current round.
check_assertion_15() {
  grep -q "webshop" "$OUT/01-session.txt" && \
  grep -q "NEEDS YOU" "$OUT/01-session.txt" && \
  grep -q "r3 · opencode · question in" "$OUT/01-session.txt" && \
  ! grep -q "r4 · opencode" "$OUT/01-session.txt"
}
assert 15 "01-session shows webshop NEEDS YOU and r3, not r4" check_assertion_15

# 16. the ledger needs_you false -> true transition (status-1 -> status-2)
#     raises the NEEDS YOU toast, or at least the badge count.
check_assertion_16() {
  grep -qi "ledger needs you" "$OUT/02-after-toast.txt" || \
  grep -q "relevo 2 need you" "$OUT/02-after-toast.txt"
}
assert 16 "02-after-toast shows the ledger NEEDS YOU toast or relevo 2 need you" check_assertion_16

# 17. rule 1: 04-binding (enter on webshop, needs_you with report_round 3)
#     arrives on the report tab with no Tab pressed; 04b-landing (no report)
#     arrives on the transcript tab.
check_assertion_17() {
  grep -q "\[ report \]" "$OUT/04-binding.txt" && \
  grep -q "\[ transcript \]" "$OUT/04b-landing.txt"
}
assert 17 "04-binding selects report and 04b-landing selects transcript" check_assertion_17

# 18. the body fills the height under the header rows: on a 45-row terminal the
#     tall report fixture shows line 30 (>= 30 body lines).
assert 18 "05-binding-report shows line 30 (the body fills the height)" \
  grep -q "line 30" "$OUT/05-binding-report.txt"

# 19. scrolling survives two polls: 05b and 05c share their first body line and
#     it is not line 01.
check_assertion_19() {
  local b c
  b="$(grep -oE "line [0-9]+" "$OUT/05b-scrolled.txt" | head -1)"
  c="$(grep -oE "line [0-9]+" "$OUT/05c-after-polls.txt" | head -1)"
  [ -n "$b" ] && [ "$b" = "$c" ] && [ "$b" != "line 01" ]
}
assert 19 "05b-scrolled and 05c-after-polls keep the same scrolled position" check_assertion_19

# 20. the binding page header follows needs_you: webshop's fixture display is
#     ACTIVE but its row needs you, so the header reads NEEDS YOU.
check_assertion_20() {
  grep -qE "webshop › r3.*NEEDS YOU" "$OUT/05-binding-report.txt"
}
assert 20 "05-binding-report header shows NEEDS YOU for webshop" check_assertion_20

# 21. the round row lists each round once, ascending: r1 r2 r3 r4, with no
#     repeated number (history.json carries two webshop r4 rows).
check_assertion_21() {
  local row
  row="$(grep -oE "\[ \] round.*" "$OUT/05-binding-report.txt" | head -1)"
  [ -n "$row" ] || return 1
  printf '%s\n' "$row" | grep -qE "r1 r2 r3 r4" && \
  [ -z "$(printf '%s\n' "$row" | grep -oE "r[0-9]+" | sort | uniq -d)" ]
}
assert 21 "05-binding-report round row is r1 r2 r3 r4 with no repeat" check_assertion_21

# Mouse is not driven here: tmux send-keys cannot deliver SGR mouse events
# reliably, so the clickable-row behaviour (onMouseDown on the sidebar and
# fleet rows) is verified by inspection of tui.tsx, not by this smoke.
echo "Captures written to: $OUT"

if [ "$FAILED" -ne 0 ]; then
  echo "Smoke test FAILED"
  exit 1
fi

echo "Smoke test PASSED (all 21 assertions passed)"
exit 0
