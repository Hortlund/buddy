# Installed by install-server.sh after a generated configuration header.
set -euo pipefail
umask 077

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
daemon() { as_service "$install_dir/buddyd" -addr 127.0.0.1:7331 -data-dir "$service_home/state" "$@"; }
manager() {
  if [ "$host_os" = linux ]; then
    admin systemctl "$1" buddyd.service
  else
    case "$1" in
      start) launchctl kickstart "gui/$(id -u)/dev.buddy.hosted" ;;
      stop) launchctl kill SIGTERM "gui/$(id -u)/dev.buddy.hosted" ;;
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
    server_host=${server_host%:7331}
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
      curl --proto '=https' --proto-redir '=https' --tlsv1.2 -fsSL https://tailscale.com/install.sh -o "$setup"
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
  if [ "$connected" = 0 ]; then
    printf 'Sign in to Tailscale using the URL below (use the same account on your other devices).\n'
    if [ "$host_os" = linux ]; then admin "$ts" up; else env TAILSCALE_BE_CLI=1 "$ts" up; fi
  fi
  if [ "$host_os" = linux ]; then
    address=$(admin "$install_dir/buddyd" -setup-tailscale -addr 127.0.0.1:7331)
  else
    address=$("$install_dir/buddyd" -setup-tailscale -addr 127.0.0.1:7331)
  fi
  printf '%s\n' "$address" | as_service tee "$service_home/server-url" >/dev/null
  printf 'Private Tailscale route ready: %s\n' "$address"
  connect
}

case "${1:-help}" in
  start|stop) manager "$1" ;;
  status) daemon -host-status; printf 'Address: %s\n' "$(origin)" ;;
  logs)
    if [ "$host_os" = linux ]; then admin journalctl -u buddyd.service -n 80 -f; else tail -n 80 -F "$service_home/server.log"; fi ;;
  login)
    as_service "$install_dir/codex" -c 'cli_auth_credentials_store="file"' login --device-auth ;;
  connect) connect ;;
  pair) daemon -pair-phone -server-url "$(origin)" ;;
  tailscale) tailnet ;;
  *) printf 'Usage: buddy-server {status|start|stop|logs|login|connect|pair|tailscale}\n\nlogin      Sign in to Codex (or save an API key from Buddy).\nconnect    Refresh a private desktop access file.\npair       Show a one-time iPhone pairing QR code.\ntailscale  Set up or repair the private Tailscale route.\n' ;;
esac
