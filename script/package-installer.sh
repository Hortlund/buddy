#!/usr/bin/env bash
# Package an existing signed app. Does not install, launch, or rebuild Buddy.
set -euo pipefail
umask 077
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if [[ "${1:-}" == --help ]]; then
  echo "Usage: $0 [path/to/Buddy.app [path/to/Buddy.dmg]]"
  exit 0
fi
[[ $# -le 2 && "$(uname -s)" == Darwin ]] || { echo 'Run on macOS with at most an app and output path.' >&2; exit 2; }
app="${1:-${BUDDY_DIST_DIR:-$ROOT_DIR/dist}/Buddy.app}"
output="${2:-$ROOT_DIR/dist/releases/Buddy.dmg}"
[[ -d "$app/Contents/MacOS" && -f "$app/Contents/Info.plist" ]] || { echo "App bundle not found: $app" >&2; exit 2; }
app="$(cd "$app" && pwd -P)"
codesign --verify --deep --strict "$app"
output=$(python3 - "$output" <<'PATH'
from pathlib import Path
import sys
print(Path(sys.argv[1]).resolve())
PATH
)
case "$output" in "$app"|"$app"/*) echo 'Place the installer outside the app bundle.' >&2; exit 2;; esac
[[ "$output" == *.dmg && ! -d "$output" ]] || { echo 'Choose a .dmg file as the output.' >&2; exit 2; }
mkdir -p "$(dirname "$output")"
stage=$(mktemp -d "$(dirname "$output")/.buddy-installer.XXXXXX")
trap 'rm -rf "$stage"' EXIT
payload="$stage/payload"
mkdir -p "$payload"
# Materialize sparse/cloned files before sizing the filesystem, and preserve
# signed resources. Permissions must allow other users to copy the app.
ditto --noclone "$app" "$payload/Buddy.app"
chmod -R a+rX,go-w "$payload"
codesign --verify --deep --strict "$payload/Buddy.app"
ln -s /Applications "$payload/Applications"
cat > "$payload/How to install Buddy.txt" <<'INSTALL'
Install Buddy

1. Quit Buddy if it is already open.
2. Drag Buddy.app onto Applications. Replace the existing app if prompted.
3. Eject this disk image and open Buddy from Applications.
4. Choose Start on this Mac, or connect to your existing Buddy home.

Updates: Settings > About > Check for updates.
Updating the desktop leaves a separately hosted server in place.

Server setup and help: https://github.com/Hortlund/buddy#install-and-run
INSTALL
chmod 644 "$payload/How to install Buddy.txt"
image="$stage/Buddy.dmg"
# The newer diskutil API replaces image creation on current macOS. Older
# supported build hosts (including macos-15 CI) use the bundled hdiutil tool.
if diskutil image create from -h >/dev/null 2>&1; then
  diskutil image create from --format UDZO --volumeName Buddy "$payload" "$image"
else
  hdiutil create -volname Buddy -srcfolder "$payload" -fs APFS -format UDZO "$image"
fi
if [[ "${BUDDY_SIGN_IDENTITY:--}" != - ]]; then
  codesign --force --timestamp --sign "$BUDDY_SIGN_IDENTITY" "$image"
  codesign --verify --strict "$image"
fi
hdiutil verify "$image"
chmod 644 "$image"
mv -f "$image" "$output"
echo "Packaged $output"
