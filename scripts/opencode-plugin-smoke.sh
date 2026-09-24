#!/usr/bin/env bash
# Smoke driver for relevo OpenCode plugin (#393)
# See docs/plans/2026-09-24-opencode-tui-b1-plugin.md §5.2
#
# SC2329: `cleanup` runs from `trap ... EXIT`, and each `check_assertion_*`
# helper is invoked indirectly, as an argument to `assert`.
# shellcheck disable=SC2329
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

# 04-binding (enter on webshop)
send Enter; sleep 1.5
capture "04-binding"

# 05-binding-report (tab to report)
send Tab; sleep 1.5
capture "05-binding-report"

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
#     r4, webshop r3, ledger r2, landing r1, so the third-newest row is
#     ledger r2 (the plan's literal, webshop r3, is the fourth).
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

echo "Captures written to: $OUT"

if [ "$FAILED" -ne 0 ]; then
  echo "Smoke test FAILED"
  exit 1
fi

echo "Smoke test PASSED (all 14 assertions passed)"
exit 0
