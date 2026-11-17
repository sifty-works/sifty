#!/usr/bin/env bash
# Prints budget measures next to the targets in docs/ARCHITECTURE.md and
# exits non-zero when one is exceeded. Lines are non-blank lines that are not
# // comments. Packages whose tests are longer than their code are listed.
# Later cards add further measures (database size, model calls, etc.).
set -euo pipefail

cd "$(git -C "$(dirname "$0")" rev-parse --show-toplevel)"

# Files of this working copy only: tracked and new, never other worktrees.
files() {
  git ls-files --cached --others --exclude-standard -- "$@" \
  | grep -v '^internal/importv1/' || true
}

count() {
  # Reads file names on stdin and prints their counted lines.
  local n=0 f
  while IFS= read -r f; do
    [ -f "$f" ] || continue
    n=$((n + $(grep -cv -e '^[[:space:]]*$' -e '^[[:space:]]*//' "$f" || true)))
  done
  echo "$n"
}

fail=0
printf "%-40s %10s %10s\n" "Measure" "Actual" "Budget"
printf "%-40s %10s %10s\n" "-------" "------" "------"

be=$(files '*.go' | grep -v '_test\.go$' | count)
be_test=$(files '*_test.go' | count)
fe=$(files 'web/src/*.ts' 'web/src/*.tsx' | grep -v '\.test\.' | count)
fe_test=$(files 'web/src/*.test.ts' 'web/src/*.test.tsx' | count)

row() { printf "%-40s %10s %10s %s\n" "$1" "$2" "$3" "$4"; }
mark() { if [ "$1" -gt "$2" ]; then fail=1; echo "OVER"; fi; }

row "Backend code (lines)" "$be" "16000" "$(mark "$be" 16000)"
row "Backend tests (lines)" "$be_test" "≤ code" "$(mark "$be_test" "$be")"
row "Frontend code (lines)" "$fe" "6000" "$(mark "$fe" 6000)"
row "Frontend tests (lines)" "$fe_test" "≤ code" "$(mark "$fe_test" "$fe")"
[ "$be" -gt 16000 ] || [ "$be_test" -gt "$be" ] || [ "$fe" -gt 6000 ] || [ "$fe_test" -gt "$fe" ] && fail=1

# Per package, for information: packages whose tests are longer than their code.
for dir in $(files '*_test.go' | xargs -n1 dirname | sort -u); do
  code=$(files "$dir/*.go" | grep -v '_test\.go$' | grep -v "^$dir/.*/" | count)
  tests=$(files "$dir/*_test.go" | grep -v "^$dir/.*/" | count)
  if [ "$tests" -gt "$code" ]; then
    row "  note: tests over code in $dir" "$tests" "$code" ""
  fi
done

if [ "$fail" -ne 0 ]; then
  echo "A budget is exceeded." >&2
  exit 1
fi
