#!/usr/bin/env bash
# Run by the release workflow on a disposable macOS runner.
set -euo pipefail
umask 077
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"
for variable in BUDDY_VERSION APPLE_CERTIFICATE_P12 APPLE_CERTIFICATE_PASSWORD BUDDY_SIGN_IDENTITY APPLE_ID APPLE_TEAM_ID APPLE_APP_PASSWORD BUDDY_UPDATE_PUBLIC_KEY MYGO_UPDATER_PRIVATE_KEY; do
  [[ -n "${!variable:-}" ]] || { echo "Missing release setting: $variable" >&2; exit 1; }
done
[[ "$BUDDY_SIGN_IDENTITY" == 'Developer ID Application:'* ]] || { echo 'Use a Developer ID Application signing identity.' >&2; exit 1; }
version=$(python3 script/release.py validate-version "$BUDDY_VERSION")
stage=$(mktemp -d)
keychain="$stage/release.keychain-db"
cleanup() {
  security delete-keychain "$keychain" >/dev/null 2>&1 || true
  rm -rf "$stage"
}
trap cleanup EXIT
keychain_password=$(openssl rand -hex 24)
security create-keychain -p "$keychain_password" "$keychain"
security set-keychain-settings -lut 21600 "$keychain"
security unlock-keychain -p "$keychain_password" "$keychain"
printf '%s' "$APPLE_CERTIFICATE_P12" | base64 --decode > "$stage/certificate.p12"
security import "$stage/certificate.p12" -k "$keychain" -P "$APPLE_CERTIFICATE_PASSWORD" -T /usr/bin/codesign
security set-key-partition-list -S apple-tool:,apple:,codesign: -s -k "$keychain_password" "$keychain" >/dev/null
security list-keychains -d user -s "$keychain" login.keychain-db
export BUDDY_UPDATE_FEED=https://github.com/Hortlund/buddy/releases/latest/download/update-darwin-universal.json
export CGO_ENABLED=1
export CGO_CFLAGS=-mmacosx-version-min=12.0
export CGO_LDFLAGS=-mmacosx-version-min=12.0
./script/package-desktop.sh
mkdir -p dist/releases
archive="Buddy_v${version}_universal.zip"
./script/package-installer.sh dist/Buddy.app dist/releases/Buddy.dmg
# A single submission covers the image and its nested app (the same flow as
# MyGo's bundler). Staple the original app too for ZIP downloads and updates.
xcrun notarytool submit dist/releases/Buddy.dmg --apple-id "$APPLE_ID" --team-id "$APPLE_TEAM_ID" --password "$APPLE_APP_PASSWORD" --wait --timeout 15m
xcrun stapler staple dist/releases/Buddy.dmg
xcrun stapler validate dist/releases/Buddy.dmg
spctl --assess --type open --context context:primary-signature --verbose dist/releases/Buddy.dmg
xcrun stapler staple dist/Buddy.app
# Stapling may add ticket files under umask 077. Keep ZIP and MyGo updates
# as accessible as the installer, including these final bundle resources.
chmod -R a+rX,go-w dist/Buddy.app
xcrun stapler validate dist/Buddy.app
codesign --verify --deep --strict dist/Buddy.app
spctl --assess --type execute --verbose dist/Buddy.app
ditto -c -k --sequesterRsrc --keepParent dist/Buddy.app "dist/releases/$archive"
# MyGo uses a tar archive of the installed bundle and an Ed25519 signature of
# its SHA-256 digest. Build after stapling so the update retains Apple's ticket.
update_archive="buddy-${version}-darwin-universal.tar.gz"
python3 - "$update_archive" <<'PYARCHIVE'
from pathlib import Path
import sys, tarfile
with tarfile.open(Path('dist/releases') / sys.argv[1], 'w:gz') as archive:
    archive.add('dist/Buddy.app', arcname='Buddy.app')
PYARCHIVE
go run ./script/sign-update.go "dist/releases/$update_archive" dist/releases/update-darwin-universal.json "$version"
