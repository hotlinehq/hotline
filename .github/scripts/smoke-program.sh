#!/bin/sh
# Checks a built hotline-proxy program: it prints the expected version and accepts
# the sidecar example's config. Used by binary-build.yml on Linux.
#   usage: smoke-program.sh <program> <version>
set -eu
program=$1
version=$2

test -x "$program" || { echo "not an executable file: $program" >&2; exit 1; }

# The version command prints the program's name (hotline-proxy), the version and, in
# brackets, the commit.
case "$("$program" version)" in
  "hotline-proxy $version"*) ;;
  *) echo "unexpected version line: $("$program" version)" >&2; exit 1 ;;
esac

# The sidecar example's config. It needs no key: the proxy makes its own.
"$program" validate --config deploy/kubernetes/sidecar/hotline-proxy.yaml
echo "smoke test passed: $program $version"
