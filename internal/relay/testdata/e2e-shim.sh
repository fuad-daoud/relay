#!/bin/sh
# relay e2e agent shim (spec 2026-09-12-e2e-real-herdr §3.2). herdr's
# detection manifests report a known-kind process as idle by default, so this
# only has to exist, echo what it is told, and never write a file. Argv --
# --agent, --dangerously-skip-permissions, anything -- is ignored.
#
# relay prompts with `--wait --until working --until blocked`, so after each
# line the shim must be *seen* working for a moment: the two lines below match
# agy's spinner_working ("<braille> <word>ing") and claude's live_turn_working
# ("<glyph> <text>…") rules respectively. They are erased before the echo so
# herdr returns to done/idle and relay's quiescence check sees a still screen.
echo "shim ready"
while IFS= read -r line; do
  printf '\342\240\213 Thinking\n\342\234\273 Thinking\342\200\246\n'
  sleep 1
  printf '\033[2A\033[J'
  echo "you said: $line"
  echo "I implemented the guard clause but could not write the file."
done
