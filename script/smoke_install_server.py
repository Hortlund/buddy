#!/usr/bin/env python3
"""Test the Linux installer in Docker, with real TLS Buddy and a service-manager fixture.

No Tailscale account, model credentials, host service, or public port is used.
The fixture starts/stops the generated unit; it does not simulate an OS reboot.
"""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]


def run(*args, **kwargs):
    return subprocess.run(args, cwd=ROOT, check=True, **kwargs)


def smoke(folder, full_build=False):
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
    (folder / 'codex').write_text('#!/bin/sh\n[ "$1" = "--version" ] && echo "codex-cli 0.167.0"\nexit 0\n')
    (folder / 'codex').chmod(0o755)
    (folder / 'systemctl').write_text('''#!/bin/bash
set -eu
case "$1" in
 daemon-reload) exit 0 ;;
 enable) touch /tmp/buddy-enabled ;;
 start) : ;;
 stop) kill -TERM "$(cat /tmp/buddy-pid)"; exit 0 ;;
 *) exit 2 ;;
esac
runuser -u buddy -- env HOME=/var/lib/buddy PATH=/opt/buddy:/usr/local/bin:/usr/bin:/bin bash -c 'echo $$ > /var/lib/buddy/pid; exec /opt/buddy/buddyd -addr 127.0.0.1:7331 -data-dir /var/lib/buddy/state -workspaces-dir /var/lib/buddy/workspaces -codex /opt/buddy/codex' > /var/lib/buddy/server.log 2>&1 &
sleep .3
cp /var/lib/buddy/pid /tmp/buddy-pid
''')
    (folder / 'systemctl').chmod(0o755)
    (folder / 'tailscale').write_text('''#!/bin/sh
case "$1 $2" in
 'ip -4') echo 100.80.1.2 ;;
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
        if 'status' in self.path: value = {'BackendState':'Running','TailscaleIPs':['100.80.1.2']}
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
    (folder / 'Dockerfile').write_text('''FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends bash curl ca-certificates git python3 systemd && rm -rf /var/lib/apt/lists/*
COPY systemctl /usr/local/bin/systemctl
COPY tailscale /usr/local/bin/tailscale
COPY . /fixture
''')
    (folder / 'verify.py').write_text('''import json, os, pathlib, ssl, subprocess, time, urllib.request
p = pathlib.Path('/var/lib/buddy')
unit = pathlib.Path('/etc/systemd/system/buddyd.service').read_text()
assert 'User=buddy' in unit and 'UMask=0077' in unit
assert '-allow-remote' not in unit and '127.0.0.1:7331' in unit
assert pathlib.Path('/tmp/buddy-enabled').exists()
assert p.stat().st_mode & 0o777 == 0o700
assert (p/'state/server.token').stat().st_mode & 0o777 == 0o600
assert (p/'state').stat().st_uid != 0
owner = json.loads(pathlib.Path('/root/.buddy-server-access/owner.json').read_text())
assert owner['server'] == 'https://100.80.1.2:7331'
forward = json.loads(pathlib.Path('/tmp/tailnet-config.json').read_text())['TCP']
assert forward['7331']['TCPForward'] == '127.0.0.1:7331'
assert forward['8443']['TCPForward'] == '127.0.0.1:8000'
assert pathlib.Path('/root/.buddy-server-access/owner.json').stat().st_mode & 0o777 == 0o600
ctx = ssl.create_default_context(cafile=str(p/'state/server.crt'))
def request(path, data=None):
    req = urllib.request.Request('https://127.0.0.1:7331'+path, data=None if data is None else json.dumps(data).encode(), headers={'Authorization': 'Bearer '+(p/'state/server.token').read_text().strip(), 'Content-Type': 'application/json'})
    with urllib.request.urlopen(req, context=ctx, timeout=5) as result: return json.load(result)
assert request('/healthz')['service'] == 'buddy'
agent = request('/v1/agents', {'name': 'Installer smoke Bud'})
# Rerunning must not restart the service, rotate credentials, or lose state.
pid = (p/'pid').read_text()
subprocess.run(['bash', '/fixture/source/script/install-server.sh', '--source-dir', '/fixture/source', '--binary', '/fixture/buddyd', '--codex', '/fixture/codex', '--no-tailscale'], check=True)
assert (p/'pid').read_text() == pid
assert (p/'state/server.token').read_text().strip() == owner['token']
assert any(a['id'] == agent['id'] for a in request('/v1/workspace')['agents'])
# Remote address export keeps the server pin; the private credentials never go to stdout.
(p/'server-url').write_text('https://100.80.1.3:7331\\n')
subprocess.run(['buddy-server', 'connect'], check=True)
updated = json.loads(pathlib.Path('/root/.buddy-server-access/owner.json').read_text())
assert updated['server'] == 'https://100.80.1.3:7331' and updated['token'] == owner['token']
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
print('Verified: non-root service, private TLS access, idempotent install, phone pairing, and state across restart.')
''')
    install_flags = '' if full_build else '--binary /fixture/buddyd --codex /fixture/codex'
    (folder / 'run.sh').write_text('''#!/bin/bash
set -euo pipefail
python3 /fixture/tailnet.py &
sleep .3
bash /fixture/source/script/install-server.sh --source-dir /fixture/source INSTALL_FLAGS
/usr/bin/systemd-analyze verify /etc/systemd/system/buddyd.service
python3 /fixture/verify.py
'''.replace('INSTALL_FLAGS', install_flags))
    image = folder.name.lower()
    try:
        run('docker', 'build', '-q', '-t', image, str(folder), stdout=subprocess.DEVNULL)
        run('docker', 'run', '--rm', image, 'bash', '/fixture/run.sh')
    finally:
        subprocess.run(['docker', 'image', 'rm', image], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


if __name__ == '__main__':
    with tempfile.TemporaryDirectory(prefix='buddy-install-', dir='/tmp') as tmp:
        smoke(Path(tmp), full_build='--full-build' in sys.argv)
