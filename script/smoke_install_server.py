#!/usr/bin/env python3
"""Test the Linux installer in Docker, with real TLS Buddy and a service-manager fixture.

No Tailscale account, model credentials, host service, or public port is used.
Use --full-build --user-install to exercise sudo installation and the shared Go tree.
The fixture starts/stops the generated unit; it does not simulate an OS reboot.
"""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]


def run(*args, **kwargs):
    return subprocess.run(args, cwd=ROOT, check=True, **kwargs)


def smoke(folder, full_build=False, user_install=False, port=None):
    arch = run('docker', 'info', '--format', '{{.Architecture}}', capture_output=True,
               text=True).stdout.strip()
    arch = {'aarch64': 'arm64', 'arm64': 'arm64', 'x86_64': 'amd64'}[arch]
    run('go', 'build', '-trimpath', '-o', str(folder / 'buddyd'), './cmd/buddyd',
        env=dict(os.environ, GOOS='linux', GOARCH=arch, CGO_ENABLED='0'))
    (folder / 'source/script').mkdir(parents=True)
    if full_build:
        paths = run('git', 'ls-files', '--cached', '--others', '--exclude-standard', '-z', capture_output=True).stdout
        for item in paths.split(b'\0'):
            if not item:
                continue
            relative = Path(os.fsdecode(item))
            src = ROOT / relative
            if src.is_file():
                dest = folder / 'source' / relative
                dest.parent.mkdir(parents=True, exist_ok=True)
                dest.write_bytes(src.read_bytes())
    for name in ('install-server.sh', 'buddy-server.sh'):
        (folder / 'source/script' / name).write_bytes((ROOT / 'script' / name).read_bytes())
    (folder / 'codex').write_text('''#!/usr/bin/python3
import json, sys
if '--version' in sys.argv:
    print('codex-cli 0.167.0')
elif 'exec' in sys.argv:
    sys.stdin.read()
    reply = json.dumps({'reply': 'Installer smoke reply.'})
    print(json.dumps({'type': 'item.completed', 'item': {'type': 'agent_message', 'text': reply}}))
''')
    (folder / 'codex').chmod(0o755)
    (folder / 'systemctl').write_text('''#!/bin/bash
set -eu
case "$1" in
 show-environment) [ "${BUDDY_SMOKE_NO_SYSTEMD:-0}" = 0 ]; exit $? ;;
 daemon-reload) exit 0 ;;
 show) echo loaded; exit 0 ;;
 enable)
   if [[ "$*" == *tailscaled.service ]]; then touch /tmp/buddy-tailnet-started; exit 0; fi
   touch /tmp/buddy-enabled ;;
 start) : ;;
 stop) kill -TERM "$(cat /tmp/buddy-pid)"; exit 0 ;;
 *) exit 2 ;;
esac
address=$(awk '{for(i=1;i<NF;i++) if($i=="-addr") {print $(i+1);exit}}' /etc/systemd/system/buddyd.service)
runuser -u buddy -- env HOME=/var/lib/buddy PATH=/opt/buddy:/usr/local/bin:/usr/bin:/bin bash -c 'echo $$ > /var/lib/buddy/pid; exec /opt/buddy/buddyd -addr "$1" -data-dir /var/lib/buddy/state -workspaces-dir /var/lib/buddy/workspaces -codex /opt/buddy/codex' fixture "$address" > /var/lib/buddy/server.log 2>&1 &
sleep .3
cp /var/lib/buddy/pid /tmp/buddy-pid
''')
    (folder / 'systemctl').chmod(0o755)
    (folder / 'tailscale').write_text('''#!/bin/sh
case "$1 $2" in
 'ip -4') echo 100.80.1.2 ;;
 'up ') exit 0 ;;
 'debug local-creds') echo 'curl -u:fixture-local-api http://localhost:48791/localapi/v0/status' ;;
 *) exit 1 ;;
esac
''')
    (folder / 'tailscale').chmod(0o755)
    (folder / 'tailnet.py').write_text('''from http.server import BaseHTTPRequestHandler, HTTPServer
import json, pathlib
config = {'TCP': {'8443': {'TCPForward': '127.0.0.1:8000'}}}
class API(BaseHTTPRequestHandler):
    def log_message(self, *args): pass
    def do_GET(self):
        if 'status' in self.path: value = {'BackendState':'Running' if pathlib.Path('/tmp/buddy-tailnet-started').exists() else 'Stopped','TailscaleIPs':['100.80.1.2']}
        elif 'prefs' in self.path: value = {'ShieldsUp':False}
        else: value = config
        self.send_response(200); self.send_header('ETag', 'fixture-v1'); self.end_headers()
        self.wfile.write(json.dumps(value).encode())
    def do_POST(self):
        global config
        assert self.headers.get('If-Match') == 'fixture-v1'
        config = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        pathlib.Path('/tmp/tailnet-config.json').write_text(json.dumps(config))
        self.send_response(200); self.end_headers()
HTTPServer(('127.0.0.1',48791),API).serve_forever()
''')
    (folder / 'curl').write_text('''#!/bin/bash
[ "${BUDDY_SMOKE_OFFLINE:-0}" = 0 ] || exit 6
exec /usr/bin/curl "$@"
''')
    (folder / 'df').write_text('''#!/bin/bash
if [ "${BUDDY_SMOKE_NO_SPACE:-0}" != 0 ]; then
  printf 'Filesystem 1024-blocks Used Available Capacity Mounted on\\nfixture 100 100 0 100%% /\\n'
else exec /usr/bin/df "$@"; fi
''')
    (folder / 'apt-get').write_text('''#!/bin/bash
set -euo pipefail
printf '%s\\n' "$*" >> /tmp/buddy-package-actions
if [ "$1" = install ] && [ -f /usr/bin/gzip.fixture ]; then mv /usr/bin/gzip.fixture /usr/bin/gzip; fi
''')
    for tool in ('curl', 'df', 'apt-get'):
        (folder / tool).chmod(0o755)
    (folder / 'preflight.py').write_text('''import os, pathlib, socket, subprocess, time
for _ in range(100):
    try:
        with socket.create_connection(('127.0.0.1',7331), timeout=1): break
    except OSError: time.sleep(.05)
else: raise AssertionError('port blocker did not start')
base = ['bash', '/fixture/source/script/install-server.sh', '--binary', '/fixture/buddyd', '--codex', '/fixture/codex']
def rejected(flags, environment, message):
    output = subprocess.run(base + flags, env=dict(os.environ, **environment), capture_output=True, text=True)
    assert output.returncode != 0 and message in output.stdout + output.stderr, output.stdout + output.stderr
    assert not pathlib.Path('/opt/buddy').exists(), 'failed preflight changed the install'
    assert not pathlib.Path('/etc/systemd/system/buddyd.service').exists(), 'failed preflight created a service'
    assert subprocess.run(['id', 'buddy'], capture_output=True).returncode != 0, 'failed preflight created an account'
rejected(['--source-dir', '/fixture/source'], {'BUDDY_SMOKE_NO_SYSTEMD':'1'}, 'systemd is installed but not running')
rejected(['--source-dir', '/fixture/source', '--port', '7331'], {}, 'Port 7331 is unavailable')
rejected([], {'BUDDY_SMOKE_OFFLINE':'1'}, 'Cannot reach Buddy source on GitHub')
rejected(['--source-dir', '/fixture/source'], {'BUDDY_SMOKE_NO_SPACE':'1'}, 'Not enough free space')
print('Verified preflight: stopped systemd, occupied explicit port, offline download, and low disk all fail before installation.')
''')
    (folder / 'Dockerfile').write_text('''FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends bash curl ca-certificates git python3 systemd sudo && rm -rf /var/lib/apt/lists/*
RUN useradd --create-home --shell /bin/bash installer && printf 'installer ALL=(ALL) NOPASSWD:ALL\\n' > /etc/sudoers.d/buddy-fixture && chmod 440 /etc/sudoers.d/buddy-fixture
COPY systemctl /usr/local/bin/systemctl
COPY tailscale /usr/local/bin/tailscale
COPY curl df apt-get /usr/local/bin/
COPY . /fixture
''')
    (folder / 'verify.py').write_text('''import json, os, pathlib, ssl, subprocess, time, urllib.request, urllib.parse
p = pathlib.Path('/var/lib/buddy')
unit = pathlib.Path('/etc/systemd/system/buddyd.service').read_text()
assert 'User=buddy' in unit and 'UMask=0077' in unit
server_port = urllib.parse.urlparse((p/'server-url').read_text().strip()).port
assert '-allow-remote' not in unit and f'127.0.0.1:{server_port}' in unit
assert server_port != 7331, 'occupied port was not avoided'
assert pathlib.Path('/tmp/buddy-package-actions').exists(), 'missing dependency was not installed'
assert pathlib.Path('/usr/bin/gzip').exists()
assert pathlib.Path('/tmp/buddy-enabled').exists()
assert pathlib.Path('/tmp/buddy-tailnet-started').exists(), 'stopped Tailscale service was not started'
assert p.stat().st_mode & 0o777 == 0o700
assert (p/'state/server.token').stat().st_mode & 0o777 == 0o600
assert (p/'state').stat().st_uid != 0
owner_path = pathlib.Path('OWNER_HOME/.buddy-server-access/owner.json')
owner = json.loads(owner_path.read_text())
assert owner['server'] == f'https://100.80.1.2:{server_port}'
forward = json.loads(pathlib.Path('/tmp/tailnet-config.json').read_text())['TCP']
assert forward[str(server_port)]['TCPForward'] == f'127.0.0.1:{server_port}'
assert forward['8443']['TCPForward'] == '127.0.0.1:8000'
assert owner_path.stat().st_mode & 0o777 == 0o600
ctx = ssl.create_default_context(cafile=str(p/'state/server.crt'))
def request(path, data=None):
    req = urllib.request.Request(f'https://127.0.0.1:{server_port}'+path, data=None if data is None else json.dumps(data).encode(), headers={'Authorization': 'Bearer '+(p/'state/server.token').read_text().strip(), 'Content-Type': 'application/json', 'Idempotency-Key': str(time.monotonic_ns())})
    with urllib.request.urlopen(req, context=ctx, timeout=5) as result: return json.load(result)
assert request('/healthz')['service'] == 'buddy'
agent = request('/v1/agents', {'name': 'Installer smoke Bud'})
# A health check alone misses unreadable files in the trusted runtime tree.
def check_chat():
    chat = request('/v1/conversations/conv_buddy/messages', {'text': 'Test a reply'})
    for _ in range(100):
        task = request('/v1/tasks/' + chat['id'])
        if task['status'] == 'succeeded': break
        assert task['status'] not in ('failed', 'interrupted'), task
        time.sleep(.1)
    else: raise AssertionError('chat did not finish')
    assert any(m['text'] == 'Installer smoke reply.' for m in request('/v1/conversations/conv_buddy')['messages'])
check_chat()
# Reproduce the old installation's root-owned 0700 toolchain, then repair it.
go = pathlib.Path('/opt/buddy/go')
if go.exists():
    subprocess.run(['chmod', '-R', 'go-rwx', str(go)], check=True)

# Rerunning must not restart the service, rotate credentials, or lose state.
pid = (p/'pid').read_text()
install = ['bash', '/fixture/source/script/install-server.sh', '--source-dir', '/fixture/source', '--binary', '/fixture/buddyd', '--codex', '/fixture/codex', '--no-tailscale']
subprocess.run(INSTALL_PREFIX + install, check=True)
bad_port = subprocess.run(INSTALL_PREFIX + install + ['--port', str(server_port+1)], capture_output=True, text=True)
assert bad_port.returncode != 0 and 'already has a port' in bad_port.stderr
assert (p/'pid').read_text() == pid
assert (p/'state/server.token').read_text().strip() == owner['token']
assert any(a['id'] == agent['id'] for a in request('/v1/workspace')['agents'])
if go.exists():
    subprocess.run(['runuser', '-u', 'buddy', '--', str(go/'bin/go'), 'version'], check=True)
check_chat()
# Remote address export keeps the server pin; the private credentials never go to stdout.
(p/'server-url').write_text(f'https://100.80.1.3:{server_port}\\n')
subprocess.run(INSTALL_PREFIX + ['buddy-server', 'connect'], check=True)
updated = json.loads(owner_path.read_text())
assert updated['server'] == f'https://100.80.1.3:{server_port}' and updated['token'] == owner['token']
assert updated['certificate'] == owner['certificate']
paired = subprocess.run(['buddy-server', 'pair'], capture_output=True, text=True, check=True).stdout
assert 'buddy-pair:' in paired and owner['token'] not in paired
subprocess.run(['buddy-server', 'stop'], check=True)
for _ in range(30):
    try: os.kill(int(pid), 0)
    except ProcessLookupError: break
    time.sleep(.1)
else: raise AssertionError('server did not stop')
subprocess.run(['buddy-server', 'start'], check=True)
for _ in range(30):
    try:
        if any(a['id'] == agent['id'] for a in request('/v1/workspace')['agents']): break
    except Exception: pass
    time.sleep(.1)
else: raise AssertionError('state lost on service restart')
print('Verified: service chat reply, private TLS access, permission repair, idempotent install, phone pairing, and state across restart.')
'''.replace('OWNER_HOME', '/home/installer' if user_install else '/root').replace('INSTALL_PREFIX', repr(['runuser', '-u', 'installer', '--'] if user_install else [])))
    install_flags = '' if full_build else '--binary /fixture/buddyd --codex /fixture/codex'
    install_prefix = 'runuser -u installer --' if user_install else ''
    if port is not None: install_flags += f' --port {port}'
    (folder / 'run.sh').write_text('''#!/bin/bash
set -euo pipefail
trap 'cat /var/lib/buddy/server.log >&2 2>/dev/null || true' ERR
python3 /fixture/tailnet.py &
python3 -m http.server 7331 --bind 127.0.0.1 > /tmp/port-blocker.log 2>&1 &
sleep .3
python3 /fixture/preflight.py
# Exercise automatic dependency installation, without contacting a package mirror.
mv /usr/bin/gzip /usr/bin/gzip.fixture
INSTALL_PREFIX bash /fixture/source/script/install-server.sh --source-dir /fixture/source INSTALL_FLAGS
if [ -d /opt/buddy/go ]; then
  runuser -u buddy -- /opt/buddy/go/bin/go version
  printf 'package main\\nimport "fmt"\\nfunc main() { fmt.Println("Go toolchain accessible") }\\n' > /var/lib/buddy/permission-check.go
  chown buddy:buddy /var/lib/buddy/permission-check.go
  runuser -u buddy -- env HOME=/var/lib/buddy /opt/buddy/go/bin/go run /var/lib/buddy/permission-check.go
fi
# Exercise Buddy's real runtime validation without using model credentials.
install -m 755 /fixture/codex /opt/buddy/codex
/usr/bin/systemd-analyze verify /etc/systemd/system/buddyd.service
python3 /fixture/verify.py
'''.replace('INSTALL_FLAGS', install_flags).replace('INSTALL_PREFIX', install_prefix))
    image = folder.name.lower()
    try:
        run('docker', 'build', '-q', '-t', image, str(folder), stdout=subprocess.DEVNULL)
        run('docker', 'run', '--rm', image, 'bash', '/fixture/run.sh')
    finally:
        subprocess.run(['docker', 'image', 'rm', image], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--full-build', action='store_true')
    parser.add_argument('--user-install', action='store_true')
    parser.add_argument('--port', type=int)
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix='buddy-install-', dir='/tmp') as tmp:
        smoke(Path(tmp), full_build=args.full_build, user_install=args.user_install, port=args.port)
