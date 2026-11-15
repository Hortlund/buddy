# Installed by install-server.sh after a generated configuration header.
set -euo pipefail
umask 077
server_port=${server_port:-7331}

fail() { printf 'Buddy: %s\n' "$*" >&2; exit 1; }
admin() { if [ "$(id -u)" = 0 ]; then "$@"; else sudo "$@"; fi; }
as_service() {
  if [ "$(id -un)" = "$service_user" ]; then
    env HOME="$service_home" PATH="$runtime_path" "$@"
  elif command -v runuser >/dev/null 2>&1 && [ "$(id -u)" = 0 ]; then
    runuser -u "$service_user" -- env HOME="$service_home" PATH="$runtime_path" "$@"
  else
    sudo -H -u "$service_user" env HOME="$service_home" PATH="$runtime_path" "$@"
  fi
}
origin() { as_service cat "$service_home/server-url"; }
daemon() { as_service "$install_dir/buddyd" -addr "127.0.0.1:$server_port" -data-dir "$service_home/state" "$@"; }
managed_pid() {
  if [ "$host_os" = linux ]; then
    admin systemctl show --property=MainPID --value buddyd.service
  else
    launchctl print "gui/$(id -u)/dev.buddy.hosted" | awk '/^[[:space:]]*pid = / {print $3; exit}'
  fi
}
restart_old_service() {
  if [ "$host_os" = linux ]; then
    # Restart waits for a previously signaled process to finish stopping.
    admin systemctl restart buddyd.service
  else
    local domain="gui/$(id -u)" target="gui/$(id -u)/dev.buddy.hosted"
    if launchctl print "$target" >/dev/null 2>&1; then
      # A kickstart while the old process is exiting can leave it stopped.
      # Unregister it and wait for server.lock before loading its saved service.
      as_service "$stage/release/buddyd" -data-dir "$service_home/state" -stop-idle-service "$target" || return 1
    fi
    launchctl bootstrap "$domain" "$HOME/Library/LaunchAgents/dev.buddy.hosted.plist"
  fi
}
update_server() (
  # One updater per installation. A stale lock after power loss is deliberately
  # visible rather than allowing a second update to overwrite recovery files.
  # These live in this function's subshell, so the EXIT trap can still read
  # them on Bash 3 after the function's local scope has been unwound.
  version=${1:-} stage='' pid='' current='' next='' backup='' changed=0 stopped=0 maintenance=0
  as_service mkdir "$service_home/.update-lock" || fail 'Another update is running. If it was interrupted, review the service and remove .update-lock in the service home before retrying.'
  stage=''
  cleanup_update() {
    local result=$?
    if [ "$result" != 0 ] && [ "$stopped" = 1 ]; then
      if [ "$changed" = 0 ]; then
        if ! restart_old_service; then
          printf '\nThe previous Buddy binaries are unchanged, but the service could not be restarted. Run buddy-server start, then buddy-server logs if it still fails.\n' >&2
        fi
      else
        manager stop || true
        printf '\nBuddy update failed. Service stopped to preserve recovery data.\nPrevious binaries: %s/previous\nPrivate state backup: %s\nSee docs/releases.md for recovery; do not reinstall or delete state.\n' "$install_dir" "$backup" >&2
      fi
    fi
    [ -z "$stage" ] || as_service rm -rf "$stage"
    # On a failed candidate keep the marker: a subsequent reboot must not
    # start work before the operator reviews the retained recovery copy.
    if [ "$maintenance" = 1 ] && { [ "$result" = 0 ] || [ "$changed" = 0 ]; }; then
      as_service rmdir "$service_home/state/.update-lock"
    fi
    as_service rmdir "$service_home/.update-lock"
  }
  trap cleanup_update EXIT
  as_service test ! -e "$service_home/state/.update-lock" || fail 'A previous update needs recovery. Review the retained backup and marker before retrying.'
  if [ -z "$version" ]; then
    next=$(as_service python3 "$install_dir/release.py" latest-version)
  else
    next=$(as_service python3 "$install_dir/release.py" validate-version "$version")
  fi
  current=$(as_service "$install_dir/buddyd" -version 2>/dev/null) || current=legacy
  if [ "$current" = "$next" ]; then printf 'Buddy %s is already installed.\n' "$next"; exit 0; fi
  # Downgrades require deliberate recovery, never a routine update.
  as_service python3 - "$current" "$next" <<'VERSIONCHECK'
import re, sys
current, target = sys.argv[1:]
if re.fullmatch(r'[0-9]+\.[0-9]+\.[0-9]+', current) and tuple(map(int, target.split('.'))) < tuple(map(int, current.split('.'))):
    sys.exit('Buddy: Refusing a downgrade. Use the documented recovery procedure.')
VERSIONCHECK
  stage=$(as_service mktemp -d "$service_home/state/.update.XXXXXX")
  as_service python3 "$install_dir/release.py" download-server --version "$next" --output "$stage/release" >/dev/null
  [ "$(as_service "$stage/release/buddyd" -version)" = "$next" ] || fail 'The downloaded server has the wrong version.'
  [ "$(as_service "$stage/release/buddyworker" -version)" = "$next" ] || fail 'The downloaded worker has the wrong version.'
  as_service mkdir "$service_home/state/.update-lock"
  maintenance=1
  printf 'Updating Buddy %s → %s. Waiting for the server to stop safely…\n' "$current" "$next"
  pid=$(managed_pid)
  if [[ "$pid" =~ ^[1-9][0-9]*$ ]]; then
    if as_service "$stage/release/buddyd" -data-dir "$service_home/state" -stop-idle-pid "$pid"; then
      stopped=1
    else
      stop_result=$?
      # Exit 3 means SIGTERM succeeded but the wait failed. Ordinary failures
      # (including a busy queue) have not stopped the daemon and need no restart.
      if [ "$stop_result" = 3 ]; then stopped=1; fi
      exit "$stop_result"
    fi
  fi
  stopped=1
  manager stop
  backup="$service_home/.buddy-migration/updates/$(date -u +%Y%m%dT%H%M%SZ)-$current"
  as_service python3 "$install_dir/release.py" snapshot --state "$service_home/state" --output "$backup"
  as_service cp "$backup/buddy.db" "$stage/compatibility.db"
  as_service "$stage/release/buddyd" -check-upgrade "$stage/compatibility.db"
  # Preserve the old executable set before touching the installed files.
  if [ "$host_os" = linux ]; then
    # Codex validates the whole trusted runtime tree as the service account.
    # Recovery executables must remain readable there, just like installed ones.
    admin install -d -m 755 "$install_dir/previous"
    for file in buddyd buddyworker buddy-server.sh release.py; do
      if [ -f "$install_dir/$file" ]; then admin cp -p "$install_dir/$file" "$install_dir/previous/$file"; fi
    done
  else
    mkdir -p "$install_dir/previous"
    for file in buddyd buddyworker buddy-server.sh release.py; do
      if [ -f "$install_dir/$file" ]; then cp -p "$install_dir/$file" "$install_dir/previous/$file"; fi
    done
  fi
  changed=1
  for file in buddyd buddyworker buddy-server.sh release.py; do
    if [ "$host_os" = linux ]; then
      admin install -m 755 "$stage/release/$file" "$install_dir/.$file.next"
      admin mv -f "$install_dir/.$file.next" "$install_dir/$file"
    else
      install -m 700 "$stage/release/$file" "$install_dir/.$file.next"
      mv -f "$install_dir/.$file.next" "$install_dir/$file"
    fi
  done
  manager start
  for attempt in {1..30}; do
    if as_service python3 - "$service_home/state/server.crt" "$server_port" "$next" "$service_home/state/server.token" <<'HEALTH'
import json, ssl, sys, urllib.request
try:
    context = ssl.create_default_context(cafile=sys.argv[1])
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPSHandler(context=context))
    base = 'https://127.0.0.1:' + sys.argv[2]
    with opener.open(base + '/healthz', timeout=2) as response:
        info = json.load(response)
    assert info['service'] == 'buddy' and info['version'] == sys.argv[3]
    with open(sys.argv[4]) as source:
        token = source.read().strip()
    request = urllib.request.Request(base + '/v1/maintenance/ready', headers={'Authorization': 'Bearer ' + token})
    with opener.open(request, timeout=2) as response:
        assert json.load(response)['status'] == 'ready'
except Exception:
    sys.exit(1)
HEALTH
    then
      stopped=0
      printf 'Buddy %s is ready. Your desktop and phone connections are unchanged.\nRecovery backup: %s\n' "$next" "$backup"
      exit 0
    fi
    sleep 1
  done
  fail 'The updated server did not become ready. Check buddy-server logs.'
)
manager() {
  if [ "$host_os" = linux ]; then
    admin systemctl "$1" buddyd.service
  else
    case "$1" in
      start) launchctl kickstart "gui/$(id -u)/dev.buddy.hosted" ;;
      stop)
        if [[ "$(managed_pid)" =~ ^[1-9][0-9]*$ ]]; then
          launchctl kill SIGTERM "gui/$(id -u)/dev.buddy.hosted"
        fi ;;
    esac
  fi
}
connect() {
  daemon -host-access -server-url "$(origin)" >/dev/null
  # Copy as the invoking owner, never write into their home with root privileges.
  local owner_user owner_home server_host
  owner_user=${SUDO_USER:-$(id -un)}
  owner_home=$service_home
  if [ "$host_os" = linux ]; then
    owner_home=$(getent passwd "$owner_user" | cut -d: -f6)
  else
    owner_home=$(dscl . -read "/Users/$owner_user" NFSHomeDirectory | cut -d' ' -f2-)
  fi
  local writer='set -eu; umask 077; folder="$1/.buddy-server-access"; [ ! -L "$folder" ] || exit 1; mkdir -p "$folder"; [ "$(stat -c %u "$folder" 2>/dev/null || stat -f %u "$folder")" = "$(id -u)" ] || exit 1; chmod 700 "$folder"; output=$(mktemp "$folder/access.XXXXXX"); trap '\''rm -f "$output"'\'' EXIT; cat > "$output"; chmod 600 "$output"; mv -f "$output" "$folder/owner.json"'
  if [ "$(id -un)" = "$owner_user" ]; then
    as_service cat "$service_home/state/owner.json" | bash -c "$writer" buddy "$owner_home"
  elif command -v runuser >/dev/null 2>&1 && [ "$(id -u)" = 0 ]; then
    as_service cat "$service_home/state/owner.json" | runuser -u "$owner_user" -- bash -c "$writer" buddy "$owner_home"
  else
    as_service cat "$service_home/state/owner.json" | sudo -u "$owner_user" bash -c "$writer" buddy "$owner_home"
  fi
  printf '\nBuddy home: %s\nPrivate desktop access: %s/.buddy-server-access/owner.json\n' "$(origin)" "$owner_home"
  printf 'Copy that file to your desktop, then choose Connections → Connect to server in Buddy.\n'
  printf 'Keep this file private: it grants owner access.\n'
  if [ "$host_os" = linux ] && [[ "$(origin)" != *127.0.0.1* ]]; then
    server_host=$(origin)
    server_host=${server_host#https://}
    server_host=${server_host%:*}
    printf '\nFrom your desktop terminal:\n  scp %s@%s:%s/.buddy-server-access/owner.json ./buddy-access.json\n' "$owner_user" "$server_host" "$owner_home"
  fi
}
tailnet() {
  local ts='' candidate address
  for candidate in tailscale /Applications/Tailscale.app/Contents/MacOS/Tailscale /opt/homebrew/bin/tailscale /usr/local/bin/tailscale; do
    if command -v "$candidate" >/dev/null 2>&1; then ts=$candidate; break; fi
  done
  if [ -z "$ts" ]; then
    if [ "$host_os" = linux ]; then
      local setup
      setup=$(mktemp)
      curl --connect-timeout 10 --max-time 90 --retry 2 --proto '=https' --proto-redir '=https' --tlsv1.2 -fsSL https://tailscale.com/install.sh -o "$setup"
      admin sh "$setup"
      rm -f "$setup"
      ts=$(command -v tailscale)
    else
      fail 'Install and open Tailscale from https://tailscale.com/download/mac, sign in, then run buddy-server tailscale again.'
    fi
  fi
  local connected=0
  if [ "$host_os" = linux ]; then
    if admin "$install_dir/buddyd" -tailscale-status >/dev/null 2>&1; then connected=1; fi
  elif "$install_dir/buddyd" -tailscale-status >/dev/null 2>&1; then connected=1; fi
  if [ "$connected" = 0 ] && [ "$host_os" = linux ]; then
    if [ "$(systemctl show -p LoadState --value tailscaled.service 2>/dev/null)" = loaded ]; then
      admin systemctl enable --now tailscaled.service
    fi
  fi
  if [ "$connected" = 0 ]; then
    printf 'Sign in to Tailscale using the URL below (use the same account on your other devices).\n'
    if [ "$host_os" = linux ]; then admin "$ts" up; else env TAILSCALE_BE_CLI=1 "$ts" up; fi
  fi
  if [ "$host_os" = linux ]; then
    address=$(admin "$install_dir/buddyd" -setup-tailscale -addr "127.0.0.1:$server_port")
  else
    address=$("$install_dir/buddyd" -setup-tailscale -addr "127.0.0.1:$server_port")
  fi
  printf '%s\n' "$address" | as_service tee "$service_home/server-url" >/dev/null
  printf 'Private Tailscale route ready: %s\n' "$address"
  connect
}

case "${1:-help}" in
  update) [ "$#" -le 2 ] || fail 'Usage: buddy-server update [vX.Y.Z]'; update_server "${2:-}" ;;
  version) as_service "$install_dir/buddyd" -version ;;
  start|stop) manager "$1" ;;
  status) daemon -host-status; printf 'Address: %s\n' "$(origin)" ;;
  logs)
    if [ "$host_os" = linux ]; then admin journalctl -u buddyd.service -n 80 -f; else tail -n 80 -F "$service_home/server.log"; fi ;;
  login)
    as_service "$install_dir/codex" -c 'cli_auth_credentials_store="file"' login --device-auth ;;
  connect) connect ;;
  pair) daemon -pair-phone -server-url "$(origin)" ;;
  tailscale) tailnet ;;
  *) printf 'Usage: buddy-server {status|version|update [vX.Y.Z]|start|stop|logs|login|connect|pair|tailscale}\n\nupdate     Install a verified stable release on this server host.\nlogin      Sign in to Codex (or save an API key from Buddy).\nconnect    Refresh a private desktop access file.\npair       Show a one-time iPhone pairing QR code.\ntailscale  Set up or repair the private Tailscale route.\n' ;;
esac
