#!/usr/bin/env bash
# Fails when the docs disagree with the binary or with each other.
#
# #54 lists README-versus-docs drift as an open item with the note "they have
# drifted before". They had, twice, and each time it was closed by reading.
# Reading does not stick — the last one was `-serve-sts`, named in four doc
# pages and in no README, while being the flag that decides whether the
# KubeMicroVM operator starts at all.
#
# So the checks below are the ones with a single right answer:
#
#   1. Every runtime flag the docs name is a flag the binary has.
#   2. Every runtime flag the binary has is named somewhere in the docs.
#   3. The operation counts quoted in prose match what m80 reports.
#
# Not checked: that the README repeats what the docs say. It deliberately does
# not — the README is a lead-in and `docs/using-it.md` is the reference, and a
# parity rule between them would fight that on every commit.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

fail=0
note() { printf 'docs-consistency: %s\n' "$1" >&2; fail=1; }

# ── 1 & 2. Flags ─────────────────────────────────────────────────────────
binary_flags="$(go run ./cmd/m80 -h 2>&1 | sed -n 's/^  -\([a-z][a-z0-9-]*\).*/\1/p' | sort -u)"
[ -n "$binary_flags" ] || { echo "docs-consistency: could not read the binary's flags" >&2; exit 1; }

# Two readings, because the two directions want different strictness.
#
# Backticked only, for "the docs name a flag that does not exist": a loose
# match would catch a hyphenated word in prose and report it as a phantom flag.
doc_flags_strict="$(grep -rhoE '`-[a-z][a-z0-9-]*`' README.md docs/*.md \
    | tr -d '`' | sed 's/^-//' | sort -u)"

# Anywhere, for "the binary has a flag nothing documents": a flag shown inside
# a shell block without backticks is still documented, and most are.
doc_flags_loose="$(grep -rhoE '(^|[[:space:]`])-[a-z][a-z0-9-]*' README.md docs/*.md \
    | sed 's/^[[:space:]`]*//; s/^-//' | sort -u)"

for f in $doc_flags_strict; do
    printf '%s\n' "$binary_flags" | grep -qx "$f" \
        || note "docs name a flag the binary does not have: -$f"
done

for f in $binary_flags; do
    printf '%s\n' "$doc_flags_loose" | grep -qx "$f" \
        || note "the binary has -$f and no doc page names it"
done

# ── 3. Operation counts ──────────────────────────────────────────────────
# "29 operations", "29/29", "all 29" — one number, several places to say it.
implemented="$(grep -cE '^\s*\{"' internal/api/routes.go)"
# Only the phrasings that claim the total. A bare N/N matches ports, dates and
# versions, and a bare "N operations" matches a per-family sub-count, both of
# which are legitimate and neither of which is this claim.
totals="$(grep -rhoE '[0-9]+/[0-9]+ operations|all [0-9]+ operations' README.md docs/*.md \
    | grep -oE '[0-9]+' | sort -u)"
for n in $totals; do
    [ "$n" = "$implemented" ] \
        || note "docs claim $n operations in total; routes.go routes $implemented"
done

# And the per-family counts have to add up to it. Two numbers that are each
# individually plausible and do not sum is exactly the drift nobody notices.
family_sum="$(grep -rhoE '^### [A-Za-z ]+ — [0-9]+ operations' docs/api-surface.md \
    | grep -oE '[0-9]+' | awk '{s += $1} END {print s+0}')"
if [ "$family_sum" -gt 0 ] && [ "$family_sum" != "$implemented" ]; then
    note "api-surface.md's per-family counts sum to $family_sum; routes.go routes $implemented"
fi

[ "$fail" -eq 0 ] && echo "docs-consistency: docs and binary agree"
exit "$fail"
