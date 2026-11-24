#!/usr/bin/env bash
# changed-scenarios.sh BASE prints the scenarios of the proxy's feature files
# that this branch adds or changes since BASE, one "features/NAME.feature:LINE"
# per line, LINE being where the scenario starts. A change to a feature
# file's Background or header prints the whole file, since it changes every
# scenario in it. Scenarios in files tagged @e2e are left out: they run
# against the built program, in make e2e.
#
# The repeat job runs these again and again, so a new scenario that passes
# only sometimes fails before it is merged (docs/flakes.md).
set -euo pipefail

base="$1"
cd "$(git rev-parse --show-toplevel)/src/hotline-proxy"

git diff --name-only --diff-filter=AM "$base" -- 'features/*.feature' | sed 's|^src/hotline-proxy/||' | while read -r file; do
  if head -n 1 "$file" | grep -q '@e2e'; then
    continue
  fi
  # The lines of the new file that this branch added or changed.
  changed=$(git diff -U0 "$base" -- "$file" | awk '
    /^@@/ {
      split($3, a, ",")
      start = substr(a[1], 2)
      count = (a[2] == "") ? 1 : a[2]
      # Lines only taken away: the line above them is in the same place.
      if (count == 0) print start
      for (i = 0; i < count; i++) print start + i
    }')
  [ -n "$changed" ] || continue
  # Each changed line belongs to the scenario that starts above it. A line
  # in the header, a Background or a Rule's own lines changes every
  # scenario after it, so the whole file runs.
  awk -v file="$file" -v changed="$changed" '
    BEGIN { n = split(changed, list, "\n"); for (i = 1; i <= n; i++) want[list[i]] = 1 }
    /^[[:space:]]*(Scenario( Outline)?|Example):/ { start = NR }
    /^[[:space:]]*(Background|Rule):/ { start = 0 }
    want[NR] {
      if (start == 0) { whole = 1 } else { hit[start] = 1 }
    }
    END {
      if (whole) { print file; exit }
      for (s in hit) print file ":" s
    }' "$file"
done | sort -u
