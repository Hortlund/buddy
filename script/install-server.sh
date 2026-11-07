#!/usr/bin/env bash
# Install Buddy on an always-on Linux PC/VPS or a signed-in Mac.
set -euo pipefail
umask 077

fail() { printf 'Buddy setup: %s\n' "$*" >&2; exit 1; }
info() { printf '\n→ %s\n' "$*"; }
admin() { if [ "$(id -u)" = 0 ]; then "$@"; else sudo "$@"; fi; }
fetch() { curl --connect-timeout 10 --max-time 300 --speed-limit 1024 --speed-time 30 --proto '=https' --proto-redir '=https' --tlsv1.2 -fL --retry 3 "$1" -o "$2"; }
verify() {
  local actual
  if command -v sha256sum >/dev/null 2>&1; then actual=$(sha256sum "$1"); else actual=$(shasum -a 256 "$1"); fi
  [ "${actual%% *}" = "$2" ] || fail "Checksum failed for $(basename "$1")."
}
provider_check=0
source_dir='' binary='' codex_binary='' ref=main use_tailscale=1
requested_port=7331 auto_port=1

# Bind rather than just inspect listeners: this also catches denied or reserved ports.
choose_port() {
  python3 - "$requested_port" "$auto_port" <<'PORTCHECK'
import errno, socket, sys
start, automatic = int(sys.argv[1]), sys.argv[2] == '1'
for port in range(start, min(start + (64 if automatic else 1), 65536)):
    try:
        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as probe:
            # Match Go's listener so closed connections in TIME_WAIT can be reused.
            probe.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            probe.bind(('127.0.0.1', port))
        print(port)
        break
    except OSError as problem:
        if problem.errno not in (errno.EADDRINUSE, errno.EACCES):
            sys.exit(f'Buddy setup: Cannot bind a local port: {problem}')
else:
    sys.exit(f'Buddy setup: Port {start} is unavailable. Use --port with a free port, or omit it for automatic selection. No running app was stopped.')
PORTCHECK
}
check_disk() {
  local available
  available=$(df -Pk "$1" | awk 'NR==2 {print $4}')
  [[ "$available" =~ ^[0-9]+$ ]] || fail "Could not check free space at $1."
  [ "$available" -ge "$2" ] || fail "Not enough free space at $1. Need at least $(( $2 / 1024 )) MiB for setup. Free space and rerun."
}
check_network() {
  local label=$1 address=$2
  printf '  Checking %s…\n' "$label"
  curl --proto '=https' --proto-redir '=https' --tlsv1.2 --connect-timeout 8 --max-time 20 -fsSLI "$address" >/dev/null ||
    fail "Cannot reach $label over HTTPS. Check DNS, outbound access, proxy settings, CA certificates, and the system clock, then rerun."
}
check_write() {
  local probe
  probe=$(admin mktemp "$1/.buddy-write-check.XXXXXX") || fail "Cannot write to $1. Check mount and directory permissions."
  admin rm -f "$probe"
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --no-tailscale) use_tailscale=0; shift ;;
    --source-dir|--binary|--codex|--ref|--port)
      [ "$#" -ge 2 ] || fail "Missing value for $1"
      case "$1" in --source-dir) source_dir=$2 ;; --binary) binary=$2 ;; --codex) codex_binary=$2 ;; --ref) ref=$2 ;; --port) requested_port=$2; auto_port=0 ;; esac
      shift 2 ;;
    -h|--help)
      printf 'Usage: install-server.sh [--no-tailscale] [--ref TAG_OR_COMMIT] [--port PORT]\n\nInstalls Buddy + Codex + Go, enables a managed service, and sets up private Tailscale access.\nLinux: systemd, dedicated buddy user. macOS: runs at login while the Mac stays awake.\nUse --source-dir PATH, --binary PATH, or --codex PATH for local/offline development.\nChecks dependencies, service support, free space, network access, and local ports.\nThe default port is selected automatically; --port requires that exact free port.\nRerunning preserves an existing installation and its data.\n'; exit 0 ;;
    *) fail "Unknown option: $1" ;;
  esac
done
[[ "$ref" =~ ^[a-zA-Z0-9][a-zA-Z0-9._/-]*$ ]] || fail 'Invalid source ref.'
[[ "$requested_port" =~ ^[1-9][0-9]{0,4}$ ]] && [ "$requested_port" -ge 1024 ] && [ "$requested_port" -le 65535 ] || fail 'Use a port from 1024 to 65535.'
host_os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$(uname -m)" in x86_64|amd64) arch=amd64; codex_arch=x86_64 ;; aarch64|arm64) arch=arm64; codex_arch=aarch64 ;; *) fail 'Supported architectures: x86_64 and arm64.' ;; esac
case "$host_os" in
  linux)
    codex_target=unknown-linux-musl
    export PATH="$PATH:/usr/sbin:/sbin"
    command -v systemctl >/dev/null 2>&1 || fail 'Linux needs systemd (on Windows use WSL2 with systemd enabled).'
    if [ "$(id -u)" != 0 ]; then command -v sudo >/dev/null || fail 'Install sudo or run as root.'; sudo -v; fi
    install_dir=/opt/buddy
    service_home=/var/lib/buddy
    service_user=buddy
    service_file=/etc/systemd/system/buddyd.service
    command_file=/usr/local/bin/buddy-server
    admin systemctl show-environment >/dev/null 2>&1 || fail 'systemd is installed but not running. Enable systemd in WSL2 and restart the distro, or use a Linux host booted with systemd.'
    missing=0
    for dependency in curl git tar gzip sha256sum python3 useradd runuser getent install mktemp df awk; do
      command -v "$dependency" >/dev/null 2>&1 || missing=1
    done
    if [ ! -s /etc/ssl/certs/ca-certificates.crt ] && [ ! -s /etc/pki/tls/certs/ca-bundle.crt ]; then missing=1; fi
    if [ "$missing" = 1 ]; then
      info 'Installing missing prerequisites'
      if command -v apt-get >/dev/null; then
        admin apt-get update
        admin apt-get install -y curl ca-certificates git tar gzip coreutils python3 util-linux passwd gawk
      elif command -v dnf >/dev/null; then
        admin dnf install -y curl ca-certificates git tar gzip coreutils python3 util-linux shadow-utils glibc-common gawk
      else fail 'Install curl, CA certificates, git, tar, gzip, coreutils, Python 3, and user-management tools, then rerun.'; fi
    fi
    for dependency in curl git tar gzip sha256sum python3 useradd runuser getent install mktemp df awk; do
      command -v "$dependency" >/dev/null 2>&1 || fail "Required dependency still missing: $dependency."
    done
    python3 -c 'import socket' || fail 'Python 3 cannot load its standard library.'
    if [ ! -d /usr/local/bin ]; then admin install -d -m 755 /usr/local/bin; fi
    check_write /opt
    check_write /var/lib
    check_write /usr/local/bin
    ;;
  darwin)
    codex_target=apple-darwin
    [ "$(id -u)" != 0 ] || fail 'Run this as your normal macOS user, without sudo.'
    service_user=$(id -un)
    service_home="$HOME/.local/share/buddy-server"
    install_dir="$service_home/bin"
    service_file="$HOME/Library/LaunchAgents/dev.buddy.hosted.plist"
    command_file="$HOME/.local/bin/buddy-server"
    launchctl print "gui/$(id -u)" >/dev/null 2>&1 || fail 'Sign in to a macOS desktop session before installing the login service.'
    command -v git >/dev/null && git --version >/dev/null || fail 'Install the Apple command line tools: xcode-select --install, then rerun.'
    for dependency in curl tar gzip shasum python3 install mktemp df awk; do
      command -v "$dependency" >/dev/null 2>&1 || fail "Missing $dependency. Install the Apple command line tools or the missing tool, then rerun."
    done
    python3 -c 'import socket' || fail 'Install working Python 3 / Apple command line tools, then rerun.'
    [ -w "$HOME" ] || fail 'Your home directory is not writable.'
    ;;
  *) fail 'Use Linux with systemd, macOS, or Windows through WSL2.' ;;
esac

[ -z "$source_dir" ] || [ -d "$source_dir" ] || fail 'The source directory does not exist.'
[ -z "$binary" ] || [ -x "$binary" ] || fail 'The supplied Buddy binary is not executable.'
[ -z "$codex_binary" ] || [ -x "$codex_binary" ] || fail 'The supplied Codex binary is not executable.'
[ -w "$HOME" ] || fail 'Your home directory is not writable.'
info 'Checking this host'
# Re-check the managed home rather than treating its live port as a conflict.
if [ -e "$service_file" ] || [ -e "$install_dir" ] || [ -e "$command_file" ] || [ -L "$command_file" ]; then
  [ -f "$install_dir/managed-install" ] && [ -x "$command_file" ] || fail "An unmanaged Buddy installation already exists. Review $install_dir and $service_file before retrying."
  if [ "$auto_port" = 0 ]; then
    stored_port=$(sed -n 's/^server_port=\([0-9][0-9]*\)$/\1/p' "$command_file")
    stored_port=${stored_port:-7331}
    [ "$requested_port" = "$stored_port" ] || fail 'This home already has a port. Keep it by omitting --port; changing it requires updating its service and client connections.'
  fi
  info 'Buddy is already installed. Keeping its binaries, service, and data.'
  if [ "$host_os" = linux ] && [ -d "$install_dir/go" ] && [ ! -L "$install_dir/go" ]; then
    admin chmod -R a+rX,go-w "$install_dir/go"
  fi
  "$command_file" status || fail "The existing home is not ready. Run $command_file start, then $command_file logs if it still fails."
  "$command_file" connect
  exit 0
fi

server_port=$(choose_port)
if [ "$server_port" != "$requested_port" ]; then
  info "Port $requested_port is busy; Buddy will use local port $server_port"
else
  printf '  Local port %s is available.\n' "$server_port"
fi

scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
space_needed=524288
if [ -z "$binary" ] || [ -z "$codex_binary" ]; then space_needed=2097152; fi
check_disk "$scratch" "$space_needed"
check_disk "$HOME" "$space_needed"
if [ "$host_os" = linux ]; then
  check_disk /opt "$space_needed"
  check_disk /var/lib 524288
else
  check_disk "$HOME" "$space_needed"
fi
if [ -z "$source_dir" ]; then check_network 'Buddy source on GitHub' "https://codeload.github.com/Hortlund/buddy/tar.gz/$ref"; fi
if [ -z "$binary" ]; then check_network 'Go downloads' "https://go.dev/dl/go1.27.3.$host_os-$arch.tar.gz"; fi
asset="codex-$codex_arch-$codex_target"
if [ -z "$codex_binary" ]; then
  check_network 'Codex downloads' "https://github.com/openai/codex/releases/download/rust-v0.167.0/$asset.tar.gz"
fi
if [ -z "$binary" ] || [ -z "$codex_binary" ]; then provider_check=1; fi

if [ -z "$source_dir" ]; then
  info "Downloading Buddy ($ref)"
  fetch "https://codeload.github.com/Hortlund/buddy/tar.gz/$ref" "$scratch/source.tar.gz"
  mkdir "$scratch/source"
  tar -xzf "$scratch/source.tar.gz" -C "$scratch/source" --strip-components=1
  source_dir="$scratch/source"
else
  source_dir=$(cd "$source_dir" && pwd -P)
fi
[ -f "$source_dir/script/buddy-server.sh" ] || fail 'Source checkout is missing script/buddy-server.sh.'

# A pinned toolchain makes installs independent of distro Go package versions.
if [ -z "$binary" ]; then
  info 'Installing Go 1.27.3 and building Buddy'
  case "$host_os/$arch" in
    linux/amd64) go_sha=65efbc80fdc1fa2cd2631cad5b481777a68e8725f34e89f50fc6ac714e379b40 ;;
    linux/arm64) go_sha=a06e99309dbcbdca1a8ccd2282c3ee146e55930886e26b4f1193269587165f49 ;;
    darwin/amd64) go_sha=b6f8e931e4ab497fcde6da8ae4499a58f9d258e55c8a80824a516eae5950a750 ;;
    darwin/arm64) go_sha=dd3908cf20959d798c1e06ebe5b72213f0505ce01373b40ce3110975a6cd99c2 ;;
  esac
  fetch "https://go.dev/dl/go1.27.3.$host_os-$arch.tar.gz" "$scratch/go.tar.gz"
  verify "$scratch/go.tar.gz" "$go_sha"
  # This archive was checksum-verified. Retain its readable/executable modes
  # when handing the toolchain to the dedicated service account on Linux.
  tar -xzpf "$scratch/go.tar.gz" -C "$scratch"
  (cd "$source_dir" && env CGO_ENABLED=0 "$scratch/go/bin/go" build -p 2 -trimpath -o "$scratch/buddyd" ./cmd/buddyd)
  binary="$scratch/buddyd"
fi
[ -x "$binary" ] || fail 'Buddy binary is not executable.'
"$binary" -h >/dev/null 2>&1 || fail 'The Buddy binary cannot run on this host. Check the OS and CPU architecture.'
if [ -z "$codex_binary" ]; then
  info 'Installing Codex 0.167.0'
  case "$host_os/$arch" in
    linux/amd64) codex_target=unknown-linux-musl; codex_sha=9d64836106d5b374e90ee4454c7ebcde7ca7106558bd7e41a34b9f26ebda251a ;;
    linux/arm64) codex_target=unknown-linux-musl; codex_sha=843489acb4d4b1079745c36b03c5963b887b83155af208fd53584e33ee8eb281 ;;
    darwin/amd64) codex_target=apple-darwin; codex_sha=d8eb4a865a0b17319cde10347523e5bab0978d01725b39f4a639f40905e01221 ;;
    darwin/arm64) codex_target=apple-darwin; codex_sha=4b22a2a971be2791c3e31f912f3e77a88ebd64a65812a7289ccba65dd6425fe9 ;;
  esac
  asset="codex-$codex_arch-$codex_target"
  fetch "https://github.com/openai/codex/releases/download/rust-v0.167.0/$asset.tar.gz" "$scratch/codex.tar.gz"
  verify "$scratch/codex.tar.gz" "$codex_sha"
  tar -xzf "$scratch/codex.tar.gz" -C "$scratch"
  codex_binary="$scratch/$asset"
fi
[ -x "$codex_binary" ] || fail 'Codex binary is not executable.'
"$codex_binary" --version >/dev/null || fail 'Codex cannot run on this host. Check the OS and CPU architecture.'
# Other programs may have started while the downloads/build ran.
server_port=$(choose_port)
info "Using local Buddy port $server_port"


[ ! -L "$service_home" ] && [ ! -L "$install_dir" ] || fail 'Installation directories must not be symlinks.'
info 'Creating the Buddy service'
if [ "$host_os" = linux ]; then
  if id buddy >/dev/null 2>&1; then
    [ "$(id -u buddy)" != 0 ] && [ "$(getent passwd buddy | cut -d: -f6)" = "$service_home" ] || fail 'Existing buddy account has an unexpected home or UID.'
  else
    admin useradd --system --user-group --create-home --home-dir "$service_home" --shell /bin/bash buddy
  fi
  admin install -d -m 700 -o buddy -g buddy "$service_home"
  admin install -d -m 755 "$install_dir"
  admin install -m 755 "$binary" "$install_dir/buddyd"
  admin install -m 755 "$codex_binary" "$install_dir/codex"
  if [ -d "$scratch/go" ]; then admin mv "$scratch/go" "$install_dir/go"; fi
  admin chown -R root:root "$install_dir"
else
  mkdir -p "$install_dir" "$(dirname "$service_file")" "$(dirname "$command_file")"
  chmod 700 "$service_home" "$install_dir"
  install -m 755 "$binary" "$install_dir/buddyd"
  install -m 755 "$codex_binary" "$install_dir/codex"
  if [ -d "$scratch/go" ]; then mv "$scratch/go" "$install_dir/go"; fi
fi
runtime_path="$install_dir:$install_dir/go/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
{
  printf '#!/usr/bin/env bash\n'
  for setting in host_os service_user service_home install_dir runtime_path server_port; do printf '%s=%q\n' "$setting" "${!setting}"; done
  cat "$source_dir/script/buddy-server.sh"
} > "$scratch/buddy-server"
if [ "$host_os" = linux ]; then
  admin install -m 755 "$scratch/buddy-server" "$command_file"
  cat > "$scratch/buddyd.service" <<UNIT
[Unit]
Description=Buddy home server
Wants=network-online.target
After=network-online.target
[Service]
Type=simple
User=buddy
Group=buddy
WorkingDirectory=$service_home
Environment=HOME=$service_home
Environment=PATH=$runtime_path
UMask=0077
ExecStart=$install_dir/buddyd -addr 127.0.0.1:$server_port -data-dir $service_home/state -workspaces-dir $service_home/workspaces -codex $install_dir/codex
Restart=on-failure
RestartSec=5s
[Install]
WantedBy=multi-user.target
UNIT
  admin install -m 644 "$scratch/buddyd.service" "$service_file"
  admin systemctl daemon-reload
  admin systemctl enable --now buddyd.service
else
  install -m 755 "$scratch/buddy-server" "$command_file"
  # XML-escape user paths, including homes with spaces or ampersands.
  xml() { printf '%s' "$1" | sed 's/\&/\&amp;/g; s/</\&lt;/g; s/>/\&gt;/g; s/"/\&quot;/g'; }
  cat > "$service_file" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>dev.buddy.hosted</string>
<key>ProgramArguments</key><array>
<string>$(xml "$install_dir/buddyd")</string><string>-addr</string><string>127.0.0.1:$server_port</string>
<string>-data-dir</string><string>$(xml "$service_home/state")</string>
<string>-workspaces-dir</string><string>$(xml "$service_home/workspaces")</string>
<string>-codex</string><string>$(xml "$install_dir/codex")</string></array>
<key>WorkingDirectory</key><string>$(xml "$service_home")</string>
<key>EnvironmentVariables</key><dict><key>HOME</key><string>$(xml "$service_home")</string><key>PATH</key><string>$(xml "$runtime_path")</string></dict>
<key>RunAtLoad</key><true/><key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
<key>ThrottleInterval</key><integer>5</integer><key>Umask</key><integer>63</integer>
<key>StandardOutPath</key><string>$(xml "$service_home/server.log")</string>
<key>StandardErrorPath</key><string>$(xml "$service_home/server.log")</string>
</dict></plist>
PLIST
  launchctl bootstrap "gui/$(id -u)" "$service_file"
fi
# Mark managed only once the service definition and management command exist.
if [ "$host_os" = linux ]; then admin touch "$install_dir/managed-install"; else touch "$install_dir/managed-install"; fi
as_service() {
  if [ "$(id -un)" = "$service_user" ]; then env HOME="$service_home" "$@";
  elif [ "$(id -u)" = 0 ] && command -v runuser >/dev/null; then runuser -u "$service_user" -- env HOME="$service_home" "$@";
  else sudo -H -u "$service_user" env HOME="$service_home" "$@"; fi
}
printf 'https://127.0.0.1:%s\n' "$server_port" | as_service tee "$service_home/server-url" >/dev/null
ready=0
for attempt in {1..30}; do
  if "$command_file" status >/dev/null 2>&1; then ready=1; break; fi
  sleep 1
done
[ "$ready" = 1 ] || fail "Service did not become ready. Run $command_file logs. Check whether local port $server_port was taken by another app during setup."
if [ "$use_tailscale" = 1 ]; then
  if ! "$command_file" tailscale; then
    printf '\nBuddy is installed locally. Finish Tailscale setup later with:\n  %s tailscale\n' "$command_file"
  fi
else
  "$command_file" connect
fi
if [ -d "$install_dir/go" ]; then
  as_service "$install_dir/go/bin/go" version >/dev/null || fail 'The service account cannot execute its Go toolchain. Check installation permissions.'
fi
as_service "$install_dir/codex" --version >/dev/null || fail 'The service account cannot execute Codex. Check installation permissions.'
if [ "$provider_check" = 1 ]; then
  provider_code=$(curl --proto '=https' --tlsv1.2 --connect-timeout 8 --max-time 15 -sS -o /dev/null -w '%{http_code}' https://api.openai.com/v1/models) || provider_code=000
  case "$provider_code" in
    200|401) printf '  OpenAI HTTPS is reachable; model authentication is checked after sign-in.\n' ;;
    *) printf '  OpenAI network check needs attention (HTTP %s). Check outbound HTTPS/proxy access before using models.\n' "$provider_code" ;;
  esac
fi
info 'Buddy is installed and running'
printf '\n  %s status\n  %s login\n  %s pair\n  %s logs\n' "$command_file" "$command_file" "$command_file" "$command_file"
printf '\nSign in to Codex with the login command, or add an OpenAI API key in Buddy → Connections.\n'
if [ "$host_os" = darwin ]; then printf 'On macOS this starts at login; keep the Mac awake and signed in. For unattended boots, use Linux.\n'; fi
printf 'Use Tailscale on each client device with the same account. No public port or Funnel is needed.\n'
