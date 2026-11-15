#!/usr/bin/env bash
# Build a bundle without starting or stopping any desktop or server.
set -euo pipefail
umask 077
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"
APP_NAME=BuddyDesktop
BUNDLE_ID=${BUDDY_BUNDLE_ID:-dev.buddy.desktop}
DIST_DIR="${BUDDY_DIST_DIR:-$ROOT_DIR/dist}"
[[ "$DIST_DIR" == /* ]] || DIST_DIR="$ROOT_DIR/$DIST_DIR"
APP_BUNDLE="$DIST_DIR/Buddy.app"
APP_BINARY="$APP_BUNDLE/Contents/MacOS/$APP_NAME"
VERSION="${BUDDY_VERSION:-0.1.0-dev+$(date -u +%Y%m%dT%H%M%SZ)}"
BUILD_NUMBER="${BUDDY_BUILD_NUMBER:-2}"
semver='^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-([0-9A-Za-z-]+\.)*[0-9A-Za-z-]+)?(\+([0-9A-Za-z-]+\.)*[0-9A-Za-z-]+)?$'
version_without_metadata="${VERSION%%+*}"
if [[ ! "$VERSION" =~ $semver ]] || [[ "${version_without_metadata#*-}" =~ (^|\.)0[0-9]+($|\.) ]]; then
  echo "BUDDY_VERSION must be a semantic version, such as 0.1.0-beta.1." >&2
  exit 2
fi
if [[ ! "$BUILD_NUMBER" =~ ^[0-9]{1,4}(\.[0-9]{1,4}){0,2}$ ]]; then
  echo "BUDDY_BUILD_NUMBER must be one to three numeric components of up to four digits." >&2
  exit 2
fi
SHORT_VERSION="${version_without_metadata%%-*}"
COMMIT="${BUDDY_COMMIT:-$(git rev-parse HEAD)}"
BUILD_DATE="${BUDDY_BUILD_DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"
common_ldflags="-X buddy/internal/buildinfo.Version=$VERSION -X buddy/internal/buildinfo.Commit=$COMMIT -X buddy/internal/buildinfo.Date=$BUILD_DATE"
app_ldflags="$common_ldflags -X main.buildVersion=$VERSION -X main.buildNumber=$BUILD_NUMBER"
# Match the update metadata used by the pinned MyGo CLI. Keep our existing
# bundler so its signed server helper and embedded helper checksum stay intact.
if [[ -n "${BUDDY_UPDATE_PUBLIC_KEY:-}" ]]; then
  [[ "${BUDDY_UPDATE_FEED:-}" == https://* && "$BUDDY_UPDATE_FEED" != *[[:space:]]* ]] || { echo "Set an HTTPS BUDDY_UPDATE_FEED." >&2; exit 2; }
  BUDDY_UPDATE_PUBLIC_KEY=$(python3 - "$BUDDY_UPDATE_PUBLIC_KEY" <<'KEY'
import base64, sys
key = sys.argv[1].strip()
assert len(base64.b64decode(key, validate=True)) == 32, 'Invalid MyGo update public key'
print(key)
KEY
)
  app_ldflags+=" -X github.com/egoist/mygo.production=1 -X github.com/egoist/mygo.packageUpdateFeed=$BUDDY_UPDATE_FEED -X github.com/egoist/mygo.packageUpdateKey=$BUDDY_UPDATE_PUBLIC_KEY"
fi
sign_args=(--sign "${BUDDY_SIGN_IDENTITY:--}")
if [[ "${BUDDY_SIGN_IDENTITY:--}" != - ]]; then sign_args+=(--options runtime --timestamp); fi

# Only a public Desktop OAuth client belongs in the distributed bundle.
gmail_client_path="${BUDDY_GMAIL_DESKTOP_CLIENT:-}"
if [[ -n "$gmail_client_path" ]]; then
  if [[ ! -f "$gmail_client_path" ]] ||
    ! gmail_client_id="$(/usr/bin/plutil -extract installed.client_id raw -expect string -o - "$gmail_client_path" 2>/dev/null)" ||
    ! gmail_client_secret="$(/usr/bin/plutil -extract installed.client_secret raw -expect string -o - "$gmail_client_path" 2>/dev/null)" ||
    [[ ! "$gmail_client_id" =~ ^[a-zA-Z0-9_-]+\.apps\.googleusercontent\.com$ || ${#gmail_client_id} -gt 256 ||
       ! "$gmail_client_secret" =~ ^[a-zA-Z0-9_-]+$ || ${#gmail_client_secret} -gt 1024 ]]; then
    echo "BUDDY_GMAIL_DESKTOP_CLIENT must point to a Google Desktop app OAuth client JSON file." >&2
    exit 2
  fi
fi

if [[ "${1:-}" == --validate ]]; then exit 0; fi
# Remove stale signed resources only after validating all inputs.
rm -rf "$APP_BUNDLE"
mkdir -p "$APP_BUNDLE/Contents/MacOS" "$APP_BUNDLE/Contents/Resources" "$APP_BUNDLE/Contents/Helpers"
if [[ "${BUDDY_UNIVERSAL:-0}" == 1 ]]; then
  for arch in arm64 amd64; do
    GOARCH="$arch" go build -trimpath -ldflags "$common_ldflags" -o "$DIST_DIR/buddyd.$arch" ./cmd/buddyd
  done
  lipo -create "$DIST_DIR/buddyd.arm64" "$DIST_DIR/buddyd.amd64" -output "$APP_BUNDLE/Contents/Helpers/buddyd"
  rm "$DIST_DIR/buddyd.arm64" "$DIST_DIR/buddyd.amd64"
else
  go build -trimpath -ldflags "$common_ldflags" -o "$APP_BUNDLE/Contents/Helpers/buddyd" ./cmd/buddyd
fi
/usr/bin/codesign --force "${sign_args[@]}" "$APP_BUNDLE/Contents/Helpers/buddyd"
helper_sha="$(/usr/bin/shasum -a 256 "$APP_BUNDLE/Contents/Helpers/buddyd" | cut -d ' ' -f 1)"
app_ldflags+=" -X main.bundledDaemonSHA256=$helper_sha"
cp "$ROOT_DIR/cmd/buddy/assets/Buddy.icns" "$APP_BUNDLE/Contents/Resources/"
gmail_resource="$APP_BUNDLE/Contents/Resources/GmailDesktopClient.json"
if [[ -n "$gmail_client_path" ]]; then
  printf '{"installed":{"client_id":"%s","client_secret":"%s"}}\n' "$gmail_client_id" "$gmail_client_secret" > "$gmail_resource"
else
  rm -f -- "$gmail_resource"
fi
if [[ "${BUDDY_UNIVERSAL:-0}" == 1 ]]; then
  for arch in arm64 amd64; do
    GOARCH="$arch" go build -trimpath -ldflags "$app_ldflags" -o "$DIST_DIR/BuddyDesktop.$arch" ./cmd/buddy
  done
  lipo -create "$DIST_DIR/BuddyDesktop.arm64" "$DIST_DIR/BuddyDesktop.amd64" -output "$APP_BINARY"
  rm "$DIST_DIR/BuddyDesktop.arm64" "$DIST_DIR/BuddyDesktop.amd64"
elif [[ -n "${BUDDY_AUTOMATION_SOCKET:-}" ]]; then
  go build -tags e2e -ldflags "$app_ldflags" -o "$APP_BINARY" ./cmd/buddy
else
  go build -ldflags "$app_ldflags" -o "$APP_BINARY" ./cmd/buddy
fi
cat > "$APP_BUNDLE/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleExecutable</key><string>$APP_NAME</string>
  <key>CFBundleName</key><string>Buddy</string>
  <key>CFBundleDisplayName</key><string>Buddy</string>
  <key>CFBundleIconFile</key><string>Buddy.icns</string>
  <key>CFBundleIdentifier</key><string>$BUNDLE_ID</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>$SHORT_VERSION</string>
  <key>CFBundleVersion</key><string>$BUILD_NUMBER</string>
  <key>LSMinimumSystemVersion</key><string>12.0</string>
  <key>NSMicrophoneUsageDescription</key><string>Buddy uses your microphone to test audio and talk to your Buds in live calls.</string>
  <key>NSPrincipalClass</key><string>NSApplication</string>
</dict>
</plist>
PLIST

/usr/bin/codesign --force --entitlements "$ROOT_DIR/deploy/desktop.entitlements" "${sign_args[@]}" "$APP_BUNDLE"
# Signing creates signature resources under our private umask. Normalize after
# signing so every distributed file remains readable in a shared app install.
chmod -R a+rX,go-w "$APP_BUNDLE"
/usr/bin/codesign --verify --deep --strict "$APP_BUNDLE"
plutil -lint "$APP_BUNDLE/Contents/Info.plist"
echo "Built $APP_BUNDLE"
