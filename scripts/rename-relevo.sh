#!/bin/sh
# scripts/rename-relevo.sh -- the one-shot mechanical rename relay -> relevo (#292).
#
# Run once, from the repository root, on a clean tree:  sh scripts/rename-relevo.sh
# It moves the relay-named paths, then rewrites relay/Relay/RELAY tokens in every
# tracked text file outside the historical records. It is kept in the tree as the
# record of what the rename did; it is not meant to be run twice (it refuses).
#
# Token rules, applied in this order to each file:
#   1. relay-site.fuad-daoud.com is protected (hostnames stay, spec §1 kept #4).
#   2. github.com/fuad-daoud/relay -> github.com/fuad-daoud/relevo (covers relay-site).
#   3. RELAY not followed by a lowercase letter -> RELEVO  (RELAY_PLANNER, RELAY).
#   4. Relay not followed by a lowercase letter -> Relevo  (Relay, RelayVerbs, findRelayBlock).
#   5. relay not followed by a lowercase letter, and either not preceded by a letter
#      or preceded by a string escape (\n, \t) -> relevo
#      (relay, relay-exit, relay/, relay.db, relay_test, relayDir, "\nrelay-exit:").
#   6. internal/harness/install_test.go's olderArchitectDoc literal is put back
#      byte for byte: it is the exact bytes of an older shipped definition, and its
#      sha must stay in internal/harness/agents/shipped.sha256.
# Verb forms (relays, relayed, relaying) are untouched by 3-5 by construction.
set -eu

[ -d cmd/relay ] || { echo "rename-relevo: cmd/relay is gone; the rename already ran" >&2; exit 1; }
[ -z "$(git status --porcelain)" ] || { echo "rename-relevo: tree is not clean" >&2; exit 1; }

# 1. Moves.
git mv cmd/relay cmd/relevo
git mv internal/relay internal/relevo
git mv dist/relay.service dist/relevo.service
git mv dist/relay-serve.service dist/relevo-serve.service
git mv dist/com.github.fuad-daoud.relay.plist.in dist/com.github.fuad-daoud.relevo.plist.in
git mv scripts/relay-service-template_test.sh scripts/relevo-service-template_test.sh

left=$(git ls-files | grep -iE 'relay' | grep -vE '^docs/(plans|specs|superpowers)/' | grep -v '^scripts/rename-relevo\.sh$' || true)
[ -z "$left" ] || { echo "rename-relevo: relay-named paths not in the move list:" >&2; echo "$left" >&2; exit 1; }

# 2. Tokens, text files only (git grep -I skips binaries).
git grep -Il -e relay -e Relay -e RELAY -- . \
	':!docs/plans/**' ':!docs/specs/**' ':!docs/superpowers/**' \
	':!go.sum' ':!scripts/rename-relevo.sh' ':!internal/harness/agents/shipped.sha256' |
	while IFS= read -r f; do
		perl -pi -e '
			s{relay-site\.fuad-daoud\.com}{\x{1}HOST\x{1}}g;
			s{github\.com/fuad-daoud/relay}{github.com/fuad-daoud/relevo}g;
			s{RELAY(?![a-z])}{RELEVO}g;
			s{Relay(?![a-z])}{Relevo}g;
			s{(?:(?<![A-Za-z])|(?<=\\[nt]))relay(?![a-z])}{relevo}g;
			s{\x{1}HOST\x{1}}{relay-site.fuad-daoud.com}g;
		' "$f"
	done

# 3. Rule 6: restore the older shipped architect definition verbatim.
perl -0777 -pi -e '
	BEGIN {
		open my $h, "-|", "git", "show", "HEAD:internal/harness/install_test.go" or die "git show: $!";
		local $/; my $old = <$h>; close $h or die "git show failed";
		($lit) = $old =~ /(const olderArchitectDoc = `[^`]*`)/ or die "olderArchitectDoc not found at HEAD";
	}
	s/const olderArchitectDoc = `[^`]*`/$lit/ or die "olderArchitectDoc not found after the sweep";
' internal/harness/install_test.go

gofmt -w cmd internal

# 4. The swept agent definitions are new shipped blobs; record their hashes
#    beside every older one (the index is append-only, #371 round 3).
sh scripts/agents-shipped.sh --write

echo "rename-relevo: done; next: go build ./... && go vet ./... && make check"
