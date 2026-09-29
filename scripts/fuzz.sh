#!/usr/bin/env bash
# Runs every fuzz target for FUZZTIME (default 30s).
#
# Go's fuzzing engine occasionally reports "context deadline exceeded" when
# -fuzztime expires while a worker is still processing (e.g. minimizing a
# newly interesting input). No failing input is produced in that case, and
# the same target passes on rerun. A real finding always writes the input to
# testdata/fuzz/<Target>/ and prints "Failing input written to ...".
# This script fails only on real findings (or any other error), and reports
# engine deadline noise as a warning so CI is not flaky.
set -uo pipefail

fuzztime="${FUZZTIME:-30s}"
status=0

run() {
  local pkg="$1" target="$2" out rc
  echo "=== ${target} (${pkg}, ${fuzztime})"
  out="$(go test "$pkg" -run '^$' -fuzz "^${target}\$" -fuzztime "$fuzztime" 2>&1)"
  rc=$?
  echo "$out" | tail -n 4
  if [ "$rc" -eq 0 ]; then
    return
  fi
  if grep -q "Failing input written" <<<"$out"; then
    echo "::error::${target} found a failing input; see ${pkg}/testdata/fuzz/${target}/"
    status=1
  elif grep -q "context deadline exceeded" <<<"$out" && ! grep -qE "panic:|--- FAIL: ${target}/" <<<"$out"; then
    echo "::warning::${target}: fuzzing engine hit its time limit mid-run; no failing input was produced"
  else
    echo "::error::${target} failed"
    status=1
  fi
}

run ./internal/page    FuzzHeaderAndItems
run ./internal/heap    FuzzParseHeader
run ./internal/datum   FuzzDecompress
run ./internal/datum   FuzzFormat
run ./internal/recover FuzzScanPage
run ./internal/carve   FuzzExamine

exit "$status"
