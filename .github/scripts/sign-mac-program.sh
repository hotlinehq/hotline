#!/usr/bin/env bash
# Signs the Mac program inside a tarball with the Developer ID certificate,
# has Apple check it (notarization), and packs the tarball again under the
# same name. Runs on a macOS runner in binary-build.yml. Apple's tools are
# used as they are: codesign, notarytool and ditto. Detail:
# docs/proxy-binaries.md.
#   usage: sign-mac-program.sh <hotline-proxy_<version>_darwin_arm64.tar.gz>
#
# Needs these in the environment (never in files, never printed):
#   APPLE_CERTIFICATE_P12        the Developer ID Application certificate and its private key, as one .p12 file, in base64
#   APPLE_CERTIFICATE_PASSWORD   the password of that file
#   APPLE_ID                     the Apple ID used for notarizing
#   APPLE_APP_PASSWORD           an app-specific password for that Apple ID
#   APPLE_TEAM_ID                the 10 character team id
set -euo pipefail

tarball=${1:?usage: sign-mac-program.sh <tarball>}

# Stop at once, naming every missing setting, before touching anything.
missing=""
for name in APPLE_CERTIFICATE_P12 APPLE_CERTIFICATE_PASSWORD APPLE_ID APPLE_APP_PASSWORD APPLE_TEAM_ID; do
  [ -n "${!name:-}" ] || missing="$missing $name"
done
if [ -n "$missing" ]; then
  echo "Missing settings:$missing" >&2
  exit 1
fi

work=$(mktemp -d)
keychain="$work/signing.keychain-db"
# Always runs, even after a failure: the keychain and the certificate file
# must not stay on the runner.
cleanup() {
  security delete-keychain "$keychain" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

tar -C "$work" -xzf "$tarball"
program="$work/hotline-proxy"
[ -f "$program" ] || { echo "The tarball has no hotline-proxy program." >&2; exit 1; }

# A new keychain with a random password, holding only this certificate.
keychain_password=$(openssl rand -hex 24)
printf '%s' "$APPLE_CERTIFICATE_P12" | base64 --decode > "$work/certificate.p12"
security create-keychain -p "$keychain_password" "$keychain"
security set-keychain-settings -lut 3600 "$keychain"
security unlock-keychain -p "$keychain_password" "$keychain"
security import "$work/certificate.p12" -k "$keychain" -P "$APPLE_CERTIFICATE_PASSWORD" -T /usr/bin/codesign
rm -f "$work/certificate.p12"
security set-key-partition-list -S apple-tool:,apple: -s -k "$keychain_password" "$keychain" >/dev/null
security list-keychains -d user -s "$keychain" login.keychain-db

# The first Developer ID Application certificate in it: the 40 character
# fingerprint names it without doubt.
identity=$(security find-identity -v -p codesigning "$keychain" | grep 'Developer ID Application' | head -1 | awk '{print $2}')
[ -n "$identity" ] || { echo "No Developer ID Application certificate in the .p12 file." >&2; exit 1; }

# The hardened runtime (--options runtime) and Apple's timestamp (--timestamp)
# are what Apple asks for before it accepts a program.
codesign --force --options runtime --timestamp --keychain "$keychain" \
  --identifier io.github.petercipov.hotline-proxy --sign "$identity" "$program"

# Stop before sending anything to Apple if the signature lacks the hardened
# runtime or our team.
codesign --verify --strict --verbose=2 "$program"
details=$(codesign -dv --verbose=4 "$program" 2>&1)
grep -q 'flags=.*runtime' <<<"$details" || { echo "The signature has no hardened runtime flag." >&2; exit 1; }
grep -q "TeamIdentifier=$APPLE_TEAM_ID" <<<"$details" || { echo "The signature names another team." >&2; exit 1; }

# Apple does not take a bare file, so it goes in a zip that is thrown away.
ditto -c -k "$program" "$work/notarize.zip"
result=$(xcrun notarytool submit "$work/notarize.zip" \
  --apple-id "$APPLE_ID" --team-id "$APPLE_TEAM_ID" --password "$APPLE_APP_PASSWORD" \
  --wait --timeout 30m 2>&1) || true
echo "$result"
if ! grep -q 'status: Accepted' <<<"$result"; then
  id=$(grep -m1 '^ *id:' <<<"$result" | awk '{print $2}' || true)
  if [ -n "$id" ]; then
    xcrun notarytool log "$id" \
      --apple-id "$APPLE_ID" --team-id "$APPLE_TEAM_ID" --password "$APPLE_APP_PASSWORD" || true
  fi
  echo "Apple did not accept the program." >&2
  exit 1
fi

# Same name, same two files: only the program inside is now the signed one.
tar -C "$work" -czf "$work/new.tar.gz" hotline-proxy LICENSE
mv "$work/new.tar.gz" "$tarball"
echo "signed and notarized: $tarball"
