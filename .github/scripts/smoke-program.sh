#!/bin/sh
# Checks a built hotline-proxy program: it prints the expected version and accepts
# the sidecar example's config. Used by binary-build.yml on Linux and macOS.
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

# The sidecar example, with its key read from a temporary folder instead of
# /etc/hotline/keys, which does not exist on a runner.
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cp -R deploy/kubernetes/sidecar "$work/config"
head -c 32 /dev/urandom > "$work/fingerprint.key"
sed "s#/etc/hotline/keys/fingerprint.key#$work/fingerprint.key#" \
  deploy/kubernetes/sidecar/hotline-proxy.yaml > "$work/config/hotline-proxy.yaml"
"$program" validate --config "$work/config/hotline-proxy.yaml"
echo "smoke test passed: $program $version"
