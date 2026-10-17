#!/usr/bin/env bash

# Run every test uncached and fail if the OpenBao tests were skipped.
set -euo pipefail

log=$(mktemp)
trap 'rm -f "$log"' EXIT

go test -count=1 -v ./... | tee "$log"

if grep -q 'bao executable not available' "$log"; then
  echo "CI requires the OpenBao tests; set TROWEL_BAO or put bao on PATH." >&2
  exit 1
fi
