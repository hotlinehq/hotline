#!/usr/bin/env bash
# check-test-waits.sh fails when a test of the proxy calls time.Sleep. A
# test never waits a fixed time and hopes something has happened by then:
# it waits with eventually, or acts out a wait with pause, both in
# src/hotline-proxy/wait_test.go (docs/flakes.md).
set -euo pipefail
cd "$(git rev-parse --show-toplevel)/src/hotline-proxy"
if found=$(grep -n 'time\.Sleep(' ./*_test.go); then
  echo "these tests call time.Sleep; wait with eventually, or act out a wait with pause (wait_test.go):"
  echo "$found"
  exit 1
fi
