# Mac signing: one-time setup

How the Mac proxy program is signed and notarized is in [proxy-binaries.md](proxy-binaries.md), decision 9. This page is what you do once, by hand, before the first signed build. The first signed run has never been tried; if a step differs from what Apple shows, fix this page.

## 1. Apple account

Sign in at developer.apple.com and accept any agreement shown (an open agreement makes notarizing fail). Note the **Team ID** on the Membership page: 10 letters and digits.

## 2. Certificate

Only the Account Holder can make it. The same certificate serves `hotline-macos-capture`, so follow section 2 of that repository's `docs/release-setup.md`: a private key and signing request with `openssl`, a certificate of type **Developer ID Application** from Certificates, Identifiers and Profiles, then a `.p12` file with an export password. Keep the key and the `.p12` private.

## 3. App-specific password

At appleid.apple.com, Sign-In and Security, App-Specific Passwords, make one for notarizing (the capture repository's `hotline-capture-notary` can be reused).

## 4. GitHub

On `petercipov/hotline`: Settings, Environments, New environment `release-signing`. Limit its deployment branches and tags to `main` and `v*`. Add these five environment secrets (the same names as in `hotline-macos-capture`; `base64 -w0 FILE` on Linux, `base64 -i FILE` on a Mac):

| Secret | Value |
| --- | --- |
| `APPLE_CERTIFICATE_P12` | base64 of the `.p12` file |
| `APPLE_CERTIFICATE_PASSWORD` | the export password |
| `APPLE_ID` | the Apple ID email |
| `APPLE_APP_PASSWORD` | the app-specific password |
| `APPLE_TEAM_ID` | the Team ID |

## 5. First check

Run **binary release** by hand from `main`, download the Mac tarball with a browser on a Mac, unpack it and run `./hotline-proxy version`, with no `xattr` step. Look at the signature with `codesign -dv --verbose=4 hotline-proxy`: it should show the hardened runtime (`flags=...runtime`) and your team.
