#!/usr/bin/env bash
# Prints budget measures next to the targets in docs/ARCHITECTURE.md.
# Later cards add further measures (database size, model calls, etc.).
set -euo pipefail

cd "$(git -C "$(dirname "$0")" rev-parse --show-toplevel)"

# Backend lines: non-blank, non-comment lines of non-test .go files,
# excluding internal/importv1.
backend_lines() {
  find . -name '*.go' \
    -not -name '*_test.go' \
    -not -path '*/internal/importv1/*' \
    -not -path '*/.dev/*' \
    -not -path '*/vendor/*' \
  | xargs grep -v '^\s*//' \
  | grep -v '^\s*$' \
  | wc -l \
  | tr -d ' '
}

# Backend test lines: non-blank lines of *_test.go files (non-comment),
# excluding internal/importv1.
backend_test_lines() {
  find . -name '*_test.go' \
    -not -path '*/internal/importv1/*' \
    -not -path '*/.dev/*' \
    -not -path '*/vendor/*' \
  | xargs grep -v '^\s*//' 2>/dev/null \
  | grep -v '^\s*$' \
  | wc -l \
  | tr -d ' '
}

# Frontend lines: non-blank lines of non-test .ts and .tsx under web/src.
frontend_lines() {
  if [ ! -d web/src ]; then
    echo 0
    return
  fi
  find web/src -name '*.ts' -o -name '*.tsx' \
  | grep -v '\.test\.' \
  | xargs grep -v '^\s*$' 2>/dev/null \
  | wc -l \
  | tr -d ' '
}

# Frontend test lines: non-blank lines of *.test.ts / *.test.tsx under web/src.
frontend_test_lines() {
  if [ ! -d web/src ]; then
    echo 0
    return
  fi
  find web/src \( -name '*.test.ts' -o -name '*.test.tsx' \) \
  | xargs grep -v '^\s*$' 2>/dev/null \
  | wc -l \
  | tr -d ' '
}

be=$(backend_lines)
be_test=$(backend_test_lines)
fe=$(frontend_lines)
fe_test=$(frontend_test_lines)

printf "%-40s %10s %10s\n" "Measure" "Actual" "Budget"
printf "%-40s %10s %10s\n" "-------" "------" "------"
printf "%-40s %10s %10s\n" "Backend code (lines)"       "$be"      "16000"
printf "%-40s %10s %10s\n" "Backend tests (lines)"      "$be_test" "≤ code"
printf "%-40s %10s %10s\n" "Frontend code (lines)"      "$fe"      "6000"
printf "%-40s %10s %10s\n" "Frontend tests (lines)"     "$fe_test" "≤ code"
