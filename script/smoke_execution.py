#!/usr/bin/env python3
"""Exercise Buddy's real stdio proxy with an installed headless Playwright MCP.

No model request, desktop interaction, personal browser, or external account.
Pass paths to a built buddyworker and an installed playwright-mcp executable.
"""
import argparse
import http.server
import json
import os
import pathlib
import queue
import re
import subprocess
import tempfile
import threading


def main():
    args = argparse.ArgumentParser()
    args.add_argument("--worker", required=True)
    args.add_argument("--provider", required=True)
    args.add_argument("--browser", required=True)
    options = args.parse_args()
    for path in (options.worker, options.provider, options.browser):
        if not os.path.isabs(path) or not os.path.isfile(path):
            args.error("Use existing absolute executable paths.")

    class Fixture(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            self.send_response(200)
            self.send_header("Content-Type", "text/html")
            self.end_headers()
            self.wfile.write(b"<title>Buddy fixture</title><h1>Ready</h1>"
                             b"<button onclick=\"document.querySelector('h1').textContent='Done'\">Run check</button>")

        def log_message(self, *_):
            pass

    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Fixture)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    try:
        with tempfile.TemporaryDirectory(prefix="buddy-execution-smoke-") as directory:
            frames = pathlib.Path(directory, "frames")
            frames.mkdir(mode=0o700)
            with open(pathlib.Path(directory, "provider.log"), "w") as log:
                child = subprocess.Popen([
                    options.worker, "automation-proxy", "-frames", str(frames),
                    "-provider", "playwright", "--", options.provider,
                    "--headless", "--isolated", "--no-webmcp", "--executable-path", options.browser,
                    "--viewport-size", "800x600", "--output-dir", directory,
                ], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=log, text=True, cwd=directory)
                responses = queue.Queue()
                def read():
                    for line in child.stdout:
                        responses.put(json.loads(line))
                threading.Thread(target=read, daemon=True).start()
                seq = 0

                def call(method, params):
                    nonlocal seq
                    seq += 1
                    child.stdin.write(json.dumps({"jsonrpc": "2.0", "id": seq, "method": method, "params": params}) + "\n")
                    child.stdin.flush()
                    while True:
                        message = responses.get(timeout=40)
                        if message.get("id") == seq:
                            assert "error" not in message, message
                            result = message["result"]
                            assert not result.get("isError"), result
                            return result

                try:
                    call("initialize", {"protocolVersion": "2024-11-05", "capabilities": {},
                                        "clientInfo": {"name": "buddy-smoke", "version": "1"}})
                    child.stdin.write('{"jsonrpc":"2.0","method":"notifications/initialized"}\n')
                    child.stdin.flush()
                    tools = call("tools/list", {})["tools"]
                    assert any(tool["name"] == "browser_navigate" for tool in tools)
                    page = call("tools/call", {"name": "browser_navigate", "arguments": {
                        "url": f"http://127.0.0.1:{server.server_port}/"}})
                    assert "Buddy fixture" in json.dumps(page), page
                    page = call("tools/call", {"name": "browser_snapshot", "arguments": {}})
                    snapshot = "\n".join(part.get("text", "") for part in page.get("content", []))
                    ref = re.search(r'button "Run check".*?\[ref=([^\]]+)\]', snapshot)
                    assert ref, snapshot
                    click = next(tool for tool in tools if tool["name"] == "browser_click")
                    target_key = "target" if "target" in click["inputSchema"]["properties"] else "ref"
                    call("tools/call", {"name": "browser_click", "arguments": {
                        "element": "Run check button", target_key: ref.group(1)}})
                    result = call("tools/call", {"name": "browser_snapshot", "arguments": {}})
                    assert "Done" in json.dumps(result), result
                    call("tools/call", {"name": "browser_take_screenshot", "arguments": {"type": "png"}})
                    frame = json.loads((frames / "frame.json").read_text())
                    assert frame["provider"] == "playwright" and len(frame["data"]) > 100
                    call("tools/call", {"name": "browser_close", "arguments": {}})
                    print(f"PASS: {len(tools)} tools discovered; navigated, clicked, verified result, captured screenshot through Buddy proxy.")
                finally:
                    child.stdin.close()
                    try:
                        child.wait(timeout=10)
                    except subprocess.TimeoutExpired:
                        child.kill()
                        child.wait()
    finally:
        server.shutdown()
        server.server_close()


if __name__ == "__main__":
    main()
