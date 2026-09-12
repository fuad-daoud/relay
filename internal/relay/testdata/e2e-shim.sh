#!/bin/sh
# relay e2e agent shim (spec 2026-09-12-e2e-real-herdr §3.2). herdr's
# detection manifest reports a known-kind process as idle by default, so this
# only has to exist, echo what it is told, and never write a file. Argv --
# --agent, --dangerously-skip-permissions, anything -- is ignored.
echo "shim ready"
while IFS= read -r line; do
  echo "you said: $line"
  echo "I implemented the guard clause but could not write the file."
done
