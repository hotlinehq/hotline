#!/usr/bin/env bash
# Checks sign-mac-program.sh without a Mac: stand-ins for Apple's tools
# (security, codesign, ditto, xcrun) write what they were called with to a
# log, so the checks can see what the script did and in which order.
#   usage: sign-mac-program-test.sh
set -u
here=$(cd "$(dirname "$0")" && pwd)
script="$here/sign-mac-program.sh"
failures=0

fail() { echo "FAIL: $1" >&2; failures=$((failures + 1)); }
pass() { echo "ok: $1"; }

# One fresh working folder per check: a tarball like the build makes, stand-in
# tools first on the path, and the five settings the script needs.
setup() {
  work=$(mktemp -d)
  mkdir -p "$work/bin" "$work/in"
  log="$work/calls.log"
  : > "$log"
  printf 'program-bytes' > "$work/in/hotline-proxy"
  printf 'licence' > "$work/in/LICENSE"
  tar -C "$work/in" -czf "$work/hotline-proxy_edge_darwin_arm64.tar.gz" hotline-proxy LICENSE
  cat > "$work/bin/security" <<'STUB'
#!/usr/bin/env bash
echo "security $*" >> "$CALLS"
case "$1" in
  find-identity) echo '  1) ABCDEF0123 "Developer ID Application: Test (TEAM123456)"' ;;
esac
STUB
  cat > "$work/bin/codesign" <<'STUB'
#!/usr/bin/env bash
echo "codesign $*" >> "$CALLS"
case "$1" in
  --force) printf 'signed' >> "${@: -1}" ;;
  -dv) echo "flags=0x10000(runtime)" >&2; echo "TeamIdentifier=TEAM123456" >&2 ;;
esac
STUB
  cat > "$work/bin/ditto" <<'STUB'
#!/usr/bin/env bash
echo "ditto $*" >> "$CALLS"
: > "${@: -1}"
STUB
  cat > "$work/bin/xcrun" <<'STUB'
#!/usr/bin/env bash
echo "xcrun $*" >> "$CALLS"
if [ "$2" = submit ]; then
  echo "  id: 11111111-2222-3333-4444-555555555555"
  echo "  status: ${NOTARY_STATUS:-Accepted}"
fi
if [ "$2" = log ]; then echo "the log from Apple"; fi
STUB
  chmod +x "$work/bin/"*
  export CALLS="$log"
  export PATH="$work/bin:$PATH"
  export APPLE_CERTIFICATE_P12=$(printf 'cert' | base64)
  export APPLE_CERTIFICATE_PASSWORD=secret-cert-password
  export APPLE_ID=person@example.com
  export APPLE_APP_PASSWORD=secret-app-password
  export APPLE_TEAM_ID=TEAM123456
}

run() { out=$(bash "$script" "$work/hotline-proxy_edge_darwin_arm64.tar.gz" 2>&1); status=$?; }

# 1. A missing setting stops the script before it touches anything, and names it.
setup
unset APPLE_APP_PASSWORD
run
[ "$status" -ne 0 ] && grep -q APPLE_APP_PASSWORD <<<"$out" && [ ! -s "$log" ] \
  && pass "a missing setting is named and nothing runs" || fail "a missing setting is named and nothing runs"

# 2. The program is signed with the hardened runtime and a timestamp, then
#    notarized, in that order.
setup
run
sign=$(grep -n '^codesign --force' "$log" | head -1 | cut -d: -f1)
notarize=$(grep -n '^xcrun notarytool submit' "$log" | head -1 | cut -d: -f1)
[ "$status" -eq 0 ] && [ -n "$sign" ] && [ -n "$notarize" ] && [ "$sign" -lt "$notarize" ] \
  && grep -q -- '--options runtime' "$log" && grep -q -- '--timestamp' "$log" \
  && grep -q -- '--identifier io.github.petercipov.hotline-proxy' "$log" \
  && pass "signed with hardened runtime and timestamp before notarizing" || fail "signed with hardened runtime and timestamp before notarizing"

# 3. The signing certificate is found in the keychain, not typed in.
grep -q -- '--sign ABCDEF0123' "$log" \
  && pass "the certificate comes from the keychain" || fail "the certificate comes from the keychain"

# 4. Notarizing waits for Apple's answer and names the team.
grep -q -- '--wait' "$log" && grep -q -- '--team-id TEAM123456' "$log" \
  && pass "notarizing waits and names the team" || fail "notarizing waits and names the team"

# 5. The tarball keeps its name and its two files, and the program inside is the signed one.
mkdir "$work/out" && tar -C "$work/out" -xzf "$work/hotline-proxy_edge_darwin_arm64.tar.gz"
[ "$(ls "$work/out" | sort | tr '\n' ' ')" = "LICENSE hotline-proxy " ] && [ "$(cat "$work/out/hotline-proxy")" = "program-bytessigned" ] \
  && pass "the tarball holds the signed program and the licence" || fail "the tarball holds the signed program and the licence"

# 6. The keychain is deleted, and no secret is printed.
grep -q '^security delete-keychain' "$log" && ! grep -q -e secret-cert-password -e secret-app-password <<<"$out" \
  && pass "the keychain is deleted and no secret is printed" || fail "the keychain is deleted and no secret is printed"

# 7. When Apple does not accept the program, the script fails, shows Apple's
#    log, deletes the keychain and leaves the tarball unsigned.
setup
export NOTARY_STATUS=Invalid
run
unset NOTARY_STATUS
mkdir "$work/out" && tar -C "$work/out" -xzf "$work/hotline-proxy_edge_darwin_arm64.tar.gz"
[ "$status" -ne 0 ] && grep -q 'the log from Apple' <<<"$out" && grep -q 'notarytool log 11111111-2222-3333-4444-555555555555' "$log" && grep -q '^security delete-keychain' "$log" \
  && [ "$(cat "$work/out/hotline-proxy")" = "program-bytes" ] \
  && pass "a refused program fails the script, shows the log and stays unsigned" || fail "a refused program fails the script, shows the log and stays unsigned"

# 8. The check after signing must see the hardened runtime flag and the team.
setup
cat > "$work/bin/codesign" <<'STUB'
#!/usr/bin/env bash
echo "codesign $*" >> "$CALLS"
case "$1" in
  --force) printf 'signed' >> "${@: -1}" ;;
  -dv) echo "flags=0x0(none)" >&2; echo "TeamIdentifier=TEAM123456" >&2 ;;
esac
STUB
run
[ "$status" -ne 0 ] && ! grep -q '^xcrun notarytool submit' "$log" \
  && pass "a program without the hardened runtime is not sent to Apple" || fail "a program without the hardened runtime is not sent to Apple"

[ "$failures" -eq 0 ] && echo "all checks passed" || { echo "$failures checks failed" >&2; exit 1; }
