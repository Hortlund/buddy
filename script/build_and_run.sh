#!/usr/bin/env bash
set -euo pipefail
umask 077

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"
MODE="${1:-run}"
APP_NAME="BuddyDesktop"
BUNDLE_ID="dev.buddy.desktop"
DIST_DIR="${BUDDY_DIST_DIR:-$ROOT_DIR/dist}"
[[ "$DIST_DIR" == /* ]] || DIST_DIR="$ROOT_DIR/$DIST_DIR"
APP_BUNDLE="$DIST_DIR/Buddy.app"
APP_BINARY="$APP_BUNDLE/Contents/MacOS/$APP_NAME"
SERVER_BINARY="$DIST_DIR/buddyd"
DATA_DIR="${BUDDY_DATA_DIR:-$ROOT_DIR/.buddy}"
PORT="${BUDDY_PORT:-7331}"
START_REPO="${BUDDY_INITIAL_REPO:-$ROOT_DIR}"
ADDRESS="127.0.0.1:$PORT"
URL="https://$ADDRESS"

case "$MODE" in
  run|--debug|--logs|--telemetry|--verify|--stop-server) ;;
  *) echo "Usage: $0 [run|--debug|--logs|--telemetry|--verify|--stop-server]" >&2; exit 2 ;;
esac

if [[ ! "$PORT" =~ ^[1-9][0-9]{0,4}$ ]] || (( PORT > 65535 )); then
  echo "BUDDY_PORT must be a port number from 1 to 65535." >&2
  exit 2
fi

mkdir -p "$DATA_DIR"
DATA_DIR="$(cd "$DATA_DIR" && pwd -P)"
DOMAIN="gui/$(id -u)"
SERVICE_ID="$(printf '%s' "$DATA_DIR" | /usr/bin/shasum -a 256 | cut -c 1-16)"
SERVICE_LABEL="dev.buddy.server.$SERVICE_ID"
if [[ -n "${BUDDY_AUTOMATION_SOCKET:-}" ]]; then
  BUNDLE_ID="$BUNDLE_ID.smoke.$SERVICE_ID"
fi
SERVICE_TARGET="$DOMAIN/$SERVICE_LABEL"
SERVICE_PLIST="$DATA_DIR/$SERVICE_LABEL.plist"
TOKEN_FILE="$DATA_DIR/server.token"
CERT_FILE="$DATA_DIR/server.crt"
HOST_FILE="$DATA_DIR/desktop-connection.json"
RETIRED_HOME="$DATA_DIR/move-retired"

is_process() {
  [[ "$1" =~ ^[1-9][0-9]*$ ]] && [[ "$(ps -p "$1" -o comm= 2>/dev/null)" == "$2" ]]
}

if [[ "$MODE" == --stop-server ]]; then
  if launchctl print "$SERVICE_TARGET" >/dev/null 2>&1; then
    launchctl bootout "$SERVICE_TARGET"
    echo "Buddy server stopped."
  else
    echo "No Buddy server is running for $DATA_DIR."
  fi
  exit 0
fi

VERSION="${BUDDY_VERSION:-0.1.0-dev+$(date -u +%Y%m%dT%H%M%SZ)}"
BUILD_NUMBER="${BUDDY_BUILD_NUMBER:-2}"
semver='^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-([0-9A-Za-z-]+\.)*[0-9A-Za-z-]+)?(\+([0-9A-Za-z-]+\.)*[0-9A-Za-z-]+)?$'
version_without_metadata="${VERSION%%+*}"
if [[ ! "$VERSION" =~ $semver ]] || [[ "${version_without_metadata#*-}" =~ (^|\.)0[0-9]+($|\.) ]]; then
  echo "BUDDY_VERSION must be a semantic version, such as 0.1.0-beta.1." >&2
  exit 2
fi
if [[ ! "$BUILD_NUMBER" =~ ^[1-9][0-9]{0,3}$ ]]; then
  echo "BUDDY_BUILD_NUMBER must be a positive integer of up to four digits." >&2
  exit 2
fi
SHORT_VERSION="${version_without_metadata%%-*}"
app_ldflags="-X main.buildVersion=$VERSION -X main.buildNumber=$BUILD_NUMBER"

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

server_health() {
  [[ -r "$CERT_FILE" ]] || return 1
  curl -q --proto '=https' --cacert "$CERT_FILE" --noproxy '*' \
    -fs --max-time 1 "$URL/healthz"
}

server_ready() {
  local health protocol
  health="$(server_health)" || return 1
  protocol="$(printf '%s' "$health" | plutil -extract desktop_protocol raw -o - - 2>/dev/null)" || return 1
  [[ "$protocol" == 75 ]]
}

if [[ ( "$MODE" == --verify || -f "$DATA_DIR/buddy.db" ) && ! -f "$HOST_FILE" && ! -e "$RETIRED_HOME" && ! -L "$RETIRED_HOME" ]] && ! server_ready; then
  upgrading=false
  if launchctl print "$SERVICE_TARGET" >/dev/null 2>&1; then
    if ! server_health >/dev/null; then
      echo "Buddy's background service is loaded but isn't responding at $URL. See $DATA_DIR/server.log or use --stop-server to restart it." >&2
      exit 1
    fi
    upgrading=true
  elif server_health >/dev/null; then
    echo "The Buddy server at $URL was started separately and needs an upgrade. Restart it with the current build and run Buddy again." >&2
    exit 1
  elif curl -q --noproxy '*' -s --max-time 1 "http://$ADDRESS/healthz" >/dev/null; then
    echo "Port $PORT is already in use by a different service or Buddy data directory. Set BUDDY_PORT to an unused port." >&2
    exit 1
  fi
  mkdir -p "$DIST_DIR"
  staged_server="$(mktemp -d "$DIST_DIR/.upgrade.XXXXXX")"
  trap '[[ ! -f "$staged_server/buddyd" ]] || unlink "$staged_server/buddyd"; rmdir "$staged_server"' EXIT
  go build -o "$staged_server/buddyd" ./cmd/buddyd
  if $upgrading; then
    "$staged_server/buddyd" -data-dir "$DATA_DIR" -stop-idle-service "$SERVICE_TARGET"
    echo "Upgrading Buddy's background service."
  fi
  mv -f "$staged_server/buddyd" "$SERVER_BINARY"
  rmdir "$staged_server"
  trap - EXIT
  touch "$DATA_DIR/server.log"
  plutil -create xml1 - > "$SERVICE_PLIST"
  plutil -insert Label -string "$SERVICE_LABEL" "$SERVICE_PLIST"
  plutil -insert ProgramArguments -array "$SERVICE_PLIST"
  server_args=("$SERVER_BINARY" -addr "$ADDRESS" -data-dir "$DATA_DIR" -runtime-dir "$DIST_DIR" -source-dir "$ROOT_DIR")
  if [[ -n "${BUDDY_WORKSPACES_DIR:-}" ]]; then
    server_args+=(-workspaces-dir "$BUDDY_WORKSPACES_DIR")
  fi
  if cli="$(command -v codex)"; then
    server_args+=(-codex "$(realpath "$cli")")
  fi
  for arg in "${server_args[@]}"; do
    plutil -insert ProgramArguments -string "$arg" -append "$SERVICE_PLIST"
  done
  plutil -insert KeepAlive -bool true "$SERVICE_PLIST"
  plutil -insert ThrottleInterval -integer 5 "$SERVICE_PLIST"
  plutil -insert ExitTimeOut -integer 10 "$SERVICE_PLIST"
  plutil -insert StandardOutPath -string "$DATA_DIR/server.log" "$SERVICE_PLIST"
  plutil -insert StandardErrorPath -string "$DATA_DIR/server.log" "$SERVICE_PLIST"
  plutil -insert EnvironmentVariables -dictionary "$SERVICE_PLIST"
  plutil -insert EnvironmentVariables.PATH -string "$PATH" "$SERVICE_PLIST"
  if [[ "${BUDDY_DECISIONS_ROUTING:-}" == 1 ]]; then
    plutil -insert EnvironmentVariables.BUDDY_DECISIONS_ROUTING -string 1 "$SERVICE_PLIST"
  fi
  launchctl bootstrap "$DOMAIN" "$SERVICE_PLIST"
  for ((attempt=0; attempt<50; attempt++)); do
    if server_ready; then
      break
    fi
    sleep 0.1
  done
  if ! server_ready; then
    launchctl bootout "$SERVICE_TARGET" || true
    echo "Buddy server didn't start. See $DATA_DIR/server.log." >&2
    exit 1
  fi
fi

# Restart only the app built by this workspace. The server keeps running.
while read -r PID; do
  if is_process "$PID" "$APP_BINARY"; then
    kill -TERM "$PID"
    for ((attempt=0; attempt<30; attempt++)); do
      if ! is_process "$PID" "$APP_BINARY"; then
        break
      fi
      sleep 0.1
    done
    if is_process "$PID" "$APP_BINARY"; then
      echo "Close Buddy and run this script again." >&2
      exit 1
    fi
  fi
done < <(pgrep -x "$APP_NAME" || true)

mkdir -p "$APP_BUNDLE/Contents/MacOS" "$APP_BUNDLE/Contents/Resources" "$APP_BUNDLE/Contents/Helpers"
go build -o "$APP_BUNDLE/Contents/Helpers/buddyd" ./cmd/buddyd
/usr/bin/codesign --force --sign - "$APP_BUNDLE/Contents/Helpers/buddyd"
helper_sha="$(/usr/bin/shasum -a 256 "$APP_BUNDLE/Contents/Helpers/buddyd" | cut -d ' ' -f 1)"
app_ldflags+=" -X main.bundledDaemonSHA256=$helper_sha"
cp "$ROOT_DIR/cmd/buddy/assets/Buddy.icns" "$APP_BUNDLE/Contents/Resources/"
gmail_resource="$APP_BUNDLE/Contents/Resources/GmailDesktopClient.json"
if [[ -n "$gmail_client_path" ]]; then
  printf '{"installed":{"client_id":"%s","client_secret":"%s"}}\n' "$gmail_client_id" "$gmail_client_secret" > "$gmail_resource"
else
  rm -f -- "$gmail_resource"
fi
if [[ -n "${BUDDY_AUTOMATION_SOCKET:-}" ]]; then
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

/usr/bin/codesign --force --sign - "$APP_BUNDLE"

APP_ARGS=(-server "$URL" -token-file "$TOKEN_FILE" -repo "$START_REPO" -local-source-dir "$ROOT_DIR" -local-path "$PATH")
if cli="$(type -P codex)"; then
  [[ "$cli" == /* ]] || cli="$ROOT_DIR/$cli"
  APP_ARGS+=(-local-codex "$cli")
fi
if [[ -n "${BUDDY_WORKSPACES_DIR:-}" ]]; then
  workspace_dir="$BUDDY_WORKSPACES_DIR"
  [[ "$workspace_dir" == /* ]] || workspace_dir="$ROOT_DIR/$workspace_dir"
  APP_ARGS+=(-local-workspaces-dir "$workspace_dir")
fi
if [[ "$MODE" == --verify ]]; then
  APP_ARGS+=(-skip-onboarding)
fi
open_app() {
  touch "$DATA_DIR/desktop.log"
  local opts=()
  if [[ -n "${BUDDY_AUTOMATION_SOCKET:-}" ]]; then
    opts+=(--env "MYGO_AUTOMATION=$BUDDY_AUTOMATION_SOCKET")
  fi
  # Bash 3 treats empty arrays as unset under nounset.
  /usr/bin/open -n --stdout "$DATA_DIR/desktop.log" --stderr "$DATA_DIR/desktop.log" ${opts[@]+"${opts[@]}"} "$APP_BUNDLE" --args "${APP_ARGS[@]}"
}

case "$MODE" in
  run) open_app ;;
  --debug) lldb -- "$APP_BINARY" "${APP_ARGS[@]}" ;;
  --logs) open_app; tail -F "$DATA_DIR/server.log" "$DATA_DIR/desktop.log" ;;
  --telemetry)
    open_app
    /usr/bin/log stream --info --style compact --predicate "process == \"$APP_NAME\" OR process == \"buddyd\""
    ;;
  --verify)
    open_app
    for ((attempt=0; attempt<30; attempt++)); do
      while read -r PID; do
        if is_process "$PID" "$APP_BINARY"; then
          if [[ -f "$HOST_FILE" ]]; then
            echo "Buddy desktop is running with a saved server connection."
            exit 0
          elif [[ -e "$RETIRED_HOME" || -L "$RETIRED_HOME" ]]; then
            echo "Buddy desktop is ready to connect to the new host."
            exit 0
          elif server_ready; then
            echo "Buddy desktop is running and the server is responding at $URL."
            exit 0
          fi
        fi
      done < <(pgrep -x "$APP_NAME" || true)
      sleep 0.1
    done
    echo "Buddy desktop or server didn't come up. See $DATA_DIR." >&2
    exit 1
    ;;
esac
