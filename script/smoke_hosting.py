#!/usr/bin/env python3
"""Move a disposable local Buddy home into Docker and reconnect its worker."""
import base64
import json
import os
from pathlib import Path
import socket
import ssl
import subprocess
import tempfile
import urllib.request

from smoke_desktop import wait_for

ROOT = Path(__file__).resolve().parents[1]


def run(*args, **kwargs):
    return subprocess.run(args, cwd=ROOT, check=True, **kwargs)


def output(*args):
    return run(*args, stdout=subprocess.PIPE, text=True).stdout.strip()


def smoke(fixture):
    state, workspaces, binaries = (fixture / name for name in ("old-state", "old-workspaces", "bin"))
    for folder in (state, workspaces, binaries):
        folder.mkdir(mode=0o700)
    old = binaries / "buddyd-local"
    run("go", "build", "-o", old, "./cmd/buddyd")
    run("go", "build", "-o", binaries / "buddyworker-local", "./cmd/buddyworker")
    architecture = {"aarch64": "arm64", "arm64": "arm64", "x86_64": "amd64", "amd64": "amd64"}[output("docker", "info", "--format", "{{.Architecture}}")]
    linux = dict(os.environ, GOOS="linux", GOARCH=architecture, CGO_ENABLED="0")
    run("go", "build", "-o", binaries / "buddyd", "./cmd/buddyd", env=linux)
    run("go", "build", "-o", binaries / "buddyworker", "./cmd/buddyworker", env=linux)
    with socket.socket() as probe:
        probe.bind(("127.0.0.1", 0))
        port = probe.getsockname()[1]

    cert = state / "server.crt"
    token = state / "server.token"

    def request(port, path, data=None, method=None):
        body = None if data is None else json.dumps(data).encode()
        headers = {"Content-Type": "application/json", "Idempotency-Key": "hosting-smoke"}
        if path != "/healthz":
            headers["Authorization"] = "Bearer " + token.read_text().strip()
        ctx = ssl.create_default_context(cafile=str(cert))
        req = urllib.request.Request(f"https://127.0.0.1:{port}{path}", data=body, headers=headers, method=method)
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPSHandler(context=ctx))
        with opener.open(req, timeout=3) as response:
            return json.load(response)

    source = subprocess.Popen([old, "-addr", f"127.0.0.1:{port}", "-data-dir", state,
                               "-workspaces-dir", workspaces, "-codex", "/missing/buddy-smoke-codex"], cwd=fixture)
    archive, owner = (fixture / name for name in ("buddy-home.tar.gz", "owner.json"))
    (fixture / "worker-config").mkdir(mode=0o700)
    worker = fixture / "worker-config/worker.json"
    try:
        wait_for("source daemon", lambda: cert.exists() and request(port, "/healthz"))
        original = request(port, "/v1/conversations/conv_buddy")
        (Path(original["agent"]["workspace"]) / "move-proof.txt").write_text("workspace survived\n")
        payload = b"private fixture attachment"
        task = request(port, "/v1/conversations/conv_buddy/messages", {
            "text": "Move this with the server",
            "attachments": [{"name": "report.txt", "data": base64.b64encode(payload).decode()}],
        })
        custom_folder = fixture / "old-custom"
        custom_folder.mkdir()
        custom = request(port, "/v1/agents", {"name": "Custom"})
        custom["workspace"] = str(custom_folder)
        request(port, f"/v1/agents/{custom['id']}", custom, "PUT")
        run(old, "-data-dir", state, "-export-connection", owner, "-server-url", f"https://127.0.0.1:{port}")
        run(binaries / "buddyworker-local", "-enroll", owner, "-connection", worker, "-name", "Docker worker")
        assert not request(port, "/v1/devices")[0]["online"]
        refused = subprocess.run([old, "-data-dir", state, "-backup", archive], capture_output=True)
        assert refused.returncode != 0 and not archive.exists(), "live server allowed backup"
    finally:
        source.terminate()
        source.wait(timeout=12)

    run(old, "-data-dir", state, "-backup", archive, "-retire-source")
    refused = subprocess.run([old, "-data-dir", state, "-addr", f"127.0.0.1:{port}"],
                             capture_output=True, timeout=5)
    assert refused.returncode != 0 and b"retired" in refused.stderr, "old home restarted"
    assert archive.stat().st_mode & 0o077 == 0, "archive was not private"
    network = fixture.name
    destination, worker_name = network + "-home", network + "-worker"
    docker_args = ["--pull=never", "--user", f"{os.getuid()}:{os.getgid()}",
                   "--mount", f"type=bind,source={fixture},target=/fixture"]
    image = os.environ.get("BUDDY_SMOKE_DOCKER_IMAGE", "alpine:3.23")
    run("docker", "run", "--rm", *docker_args, image, "/fixture/bin/buddyd",
        "-restore", "/fixture/buddy-home.tar.gz", "-restore-to", "/fixture/new-home")
    run("docker", "network", "create", network, stdout=subprocess.DEVNULL)
    try:
        run("docker", "run", "-d", "--name", destination, *docker_args, "--network", network,
            "--network-alias", "buddy-home", "-p", "127.0.0.1::7331", image, "/fixture/bin/buddyd",
            "-allow-remote", "-addr", "0.0.0.0:7331", "-data-dir", "/fixture/new-home/state",
            "-workspaces-dir", "/fixture/new-home/workspaces", "-codex", "/missing/buddy-smoke-codex",
            stdout=subprocess.DEVNULL)
        new_port = output("docker", "port", destination, "7331/tcp").rsplit(":", 1)[1]
        wait_for("restored daemon", lambda: request(new_port, "/healthz"))
        restored = request(new_port, "/v1/conversations/conv_buddy")
        assert restored["agent"]["workspace"] == "/fixture/new-home/workspaces/agt_buddy"
        assert (fixture / "new-home/workspaces/agt_buddy/move-proof.txt").read_text() == "workspace survived\n"
        assert any(m["text"] == "Move this with the server" for m in restored["messages"])
        held = request(new_port, f"/v1/agents/{custom['id']}")
        assert held["workspace_needs_review"]
        held.update(workspace="", confirm_workspace=True)
        confirmed = request(new_port, f"/v1/agents/{custom['id']}", held, "PUT")
        assert not confirmed.get("workspace_needs_review")
        assert confirmed["workspace"] == f"/fixture/new-home/workspaces/{custom['id']}"
        saved = request(new_port, f"/v1/tasks/{task['id']}/attachments/0")
        assert base64.b64decode(saved["data"]) == payload
        assert request(new_port, "/v1/runtime/codex/key")["state"] == "missing"

        run("docker", "run", "-d", "--name", worker_name, *docker_args, "--network", network,
            image, "/fixture/bin/buddyworker", "-connection", "/fixture/worker-config/worker.json",
            "-server", "https://buddy-home:7331", "-workspace", "/fixture/old-workspaces",
            "-codex", "/missing/buddy-smoke-codex", stdout=subprocess.DEVNULL)
        wait_for("paired worker after move", lambda: any(
            device["name"] == "Docker worker" and device["online"]
            for device in request(new_port, "/v1/devices")
        ))
        print("Docker move verified: source retired, custom workspace held, chat and files restored, worker reconnected.")
    finally:
        for name in (worker_name, destination):
            subprocess.run(["docker", "rm", "-f", name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        subprocess.run(["docker", "network", "rm", network], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


if __name__ == "__main__":
    with tempfile.TemporaryDirectory(prefix="buddy-hosting-", dir="/tmp") as directory:
        smoke(Path(directory).resolve())
