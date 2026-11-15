#!/usr/bin/env python3
"""Exercise Buddy's real stdio proxy with an installed headless Playwright MCP.

No model request, desktop interaction, personal browser, or external account.
Pass paths to a built buddyworker and an installed playwright-mcp executable.
"""
import argparse
import base64
import hashlib
import http.server
import json
import os
import pathlib
import queue
import re
import socket
import ssl
import struct
import subprocess
import tempfile
import threading
import time


def main():
    args = argparse.ArgumentParser()
    args.add_argument("--worker", required=True)
    args.add_argument("--provider", required=True)
    args.add_argument("--browser", required=True)
    options = args.parse_args()
    for path in (options.worker, options.provider, options.browser):
        if not os.path.isabs(path) or not os.path.isfile(path):
            args.error("Use existing absolute executable paths.")

    forbidden_hits = []
    pixel_clicks = []
    named_clicks = []
    submissions = []
    authorized_requests = []
    flip = threading.Event()
    flipped = threading.Event()
    flip_mode = "label"
    read_private = False
    read_second_origin = False
    allow_write = False
    auth_epoch = "fixture-1"
    dispatch_after = None
    cancellation_waiting = threading.Event()
    cancellation_released = threading.Event()

    held_response = threading.Event()
    release_response = threading.Event()
    socket_messages = []
    allow_socket = False
    revoked_frame_visible = True
    release_revoked_frame = threading.Event()
    cancel_post_verified = threading.Event()
    cancel_post_waiting = threading.Event()
    cancel_post_released = threading.Event()
    cancelled_posts = []
    count_download_checks = False
    download_checks = 0
    download_finished = threading.Event()
    progressive_continue = threading.Event()
    progressive_continued = []
    worker_fragments_finished = threading.Event()
    worker_fixture_finished = threading.Event()
    busy_frame_waiting = threading.Event()
    release_busy_frame = threading.Event()
    busy_frame_finished = threading.Event()

    class Forbidden(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            if self.path == "/socket":
                return self.read_socket()
            forbidden_hits.append(self.path)
            self.send_response(200)
            self.send_header("Content-Type", "text/html")
            self.send_header("Access-Control-Allow-Origin", "*")
            self.end_headers()
            if self.path == "/hold":
                self.wfile.write(b"Response started ")
                self.wfile.flush()
                held_response.set()
                if release_response.wait(10):
                    self.wfile.write(b"revoked response marker")
            elif self.path == "/history":
                self.wfile.write(b"<script>console.log('private console marker');"
                                 b"fetch('/resource?token=private-network-marker')</script><h1>History</h1>")
            else:
                self.wfile.write(b"<h1>Secret fixture marker</h1>")

        def read_socket(self):
            key = self.headers["Sec-WebSocket-Key"]
            accept = base64.b64encode(hashlib.sha1((key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").encode()).digest()).decode()
            self.protocol_version = "HTTP/1.1"
            self.send_response(101)
            self.send_header("Upgrade", "websocket")
            self.send_header("Connection", "Upgrade")
            self.send_header("Sec-WebSocket-Accept", accept)
            self.end_headers()
            self.close_connection = True
            self.connection.settimeout(8)
            try:
                header = self.rfile.read(2)
                if len(header) != 2 or header[0] & 15 != 1:
                    return
                mask, data = self.rfile.read(4), self.rfile.read(header[1] & 127)
                socket_messages.append(bytes(byte ^ mask[i % 4] for i, byte in enumerate(data)))
            except TimeoutError:
                pass

        def log_message(self, *_):
            pass

    class LanFixture(Forbidden):
        def do_GET(self):
            if self.path == "/redirect":
                self.send_response(302)
                self.send_header("Location", "/private")
                self.end_headers()
            elif self.path == "/":
                self.send_response(200)
                self.send_header("Content-Type", "text/html")
                self.end_headers()
                self.wfile.write(b"<h1>Loading</h1><script>fetch('/asset').then(r=>r.text())"
                                 b".then(text=>document.querySelector('h1').textContent=text)</script>")
            elif self.path == "/asset" and not self.headers.get("X-Buddy-Resource"):
                self.send_response(200)
                self.end_headers()
                self.wfile.write(b"LAN asset loaded")
            else:
                super().do_GET()

    lan = None
    try:
        # Resolve the local interface without sending a packet.
        with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as route:
            route.connect(("192.0.2.1", 9))
            lan_host = route.getsockname()[0]
        if not lan_host.startswith("127."):
            lan = http.server.ThreadingHTTPServer((lan_host, 0), LanFixture)
    except OSError:
        pass
    lan_origin = f"http://{lan_host}:{lan.server_port}" if lan else ""
    if lan:
        threading.Thread(target=lan.serve_forever, daemon=True).start()

    outside = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Forbidden)
    threading.Thread(target=outside.serve_forever, daemon=True).start()

    class Fixture(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            if self.path.startswith("/redirect-"):
                target = {
                    "/redirect-outside": f"http://127.0.0.1:{outside.server_port}/secret",
                    "/redirect-private": "/private",
                    "/redirect-allowed": "/destination",
                }[self.path]
                self.send_response(302)
                self.send_header("Location", target)
                self.end_headers()
                return
            if self.path == "/private":
                return Forbidden.do_GET(self)
            if self.path == "/socket-worker":
                self.send_response(200)
                self.send_header("Content-Type", "application/javascript")
                self.end_headers()
                self.wfile.write(b"onmessage=({data})=>{const ws=new WebSocket(data);ws.onopen=()=>ws.send('worker update')}")
                return
            worker_scripts = {
                "/diagnostics-worker": b"console.log('initial worker console marker');"
                                       b"onmessage=e=>{console.log(e.data);postMessage('logged')}",
                "/outer-worker": b"console.log('outer worker console marker');"
                                 b"const child=new Worker('/inner-worker');child.onmessage=e=>postMessage(e.data)",
                "/inner-worker": b"console.log('nested worker console marker');postMessage('Nested worker ran')",
            }
            if self.path in worker_scripts:
                self.send_response(200)
                self.send_header("Content-Type", "application/javascript")
                self.end_headers()
                self.wfile.write(worker_scripts[self.path])
                return
            if self.path == "/progressive":
                prefix = b"<h1>Progressive page</h1><script>fetch('/continue-progressive')</script>"
                remainder = b" " * (1 << 20) + b"<p>Progressive page finished</p>"
                self.send_response(200)
                self.send_header("Content-Type", "text/html")
                self.send_header("Content-Length", str(len(prefix) + len(remainder)))
                self.end_headers()
                self.wfile.write(prefix)
                self.wfile.flush()
                progressive_continued.append(progressive_continue.wait(3))
                self.wfile.write(remainder)
                return
            if self.path == "/continue-progressive":
                progressive_continue.set()
                self.send_response(204)
                self.end_headers()
                return
            if self.path == "/blob":
                self.send_response(200)
                self.send_header("Content-Type", "application/octet-stream")
                self.send_header("Content-Length", str(2 << 20))
                self.end_headers()
                self.wfile.write(b"x" * (2 << 20))
                return
            self.send_response(200)
            self.send_header("Content-Type", "text/html")
            self.end_headers()
            if self.path == "/revoked-frame-parent":
                self.wfile.write(b"<h1>Allowed parent</h1><iframe src='/revoked-frame'></iframe>")
                return
            if self.path == "/revoked-frame":
                self.wfile.write(b"<h1>Private frame</h1><script>console.log('initial frame console marker');"
                                 b"const worker=new Worker('/diagnostics-worker');"
                                 b"worker.onmessage=()=>setTimeout(()=>frameElement.remove(),100);"
                                 b"const timer=setInterval(async()=>{if(await (await fetch('/release-frame')).text()==='yes'){"
                                 b"clearInterval(timer);console.log('revoked frame console marker');"
                                 b"worker.postMessage('revoked worker console marker')}},200)</script>")
                return
            if self.path in {"/replace-document", "/implicit-noop-document"}:
                implicit = self.path == "/implicit-noop-document"
                replacement = """<h1>Replacement document</h1>
                    <button id='allowed'>Open replacement details</button>
                    <button id='changing' onpointerdown="this.textContent='Delete'">Change in replacement</button>
                    <script>window.addEventListener('click',e=>{
                        if(e.target.id==='allowed'||e.target.id==='changing')
                            fetch('/named?'+encodeURIComponent(e.target.textContent))
                    },true); REOPEN </script>""".replace("REOPEN", "document.open()" if implicit else "")
                payload = json.dumps(replacement).replace("</", "<" + chr(92) + "/")
                opening = "" if implicit else "document.open();"
                self.wfile.write((f"<script>setTimeout(()=>{{{opening}document.write({payload});"
                                  "document.close()},100)</script>").encode())
                return
            if self.path == "/implicit-document-console":
                self.wfile.write(b"""<h1>Waiting for replacement</h1><script>setTimeout(() => {
    document.write('<h1>Implicit document replacement</h1>');
    document.close();
    console.error('implicit replacement page error');
    fetch('/worker-fixture-finished');
}, 100)</script>""")
                return
            if self.path == "/busy-frame-parent":
                self.wfile.write(f"""<h1>Responsive parent</h1><iframe id="child" src="http://localhost:{self.server.server_port}/busy-frame-child"></iframe>
<button onclick="console.error('responsive parent console marker');child.contentWindow.postMessage('start', '*')">Block child frame</button>""".encode())
                return
            if self.path == "/busy-frame-child":
                self.wfile.write(b"""<h1>Child ready</h1><script>
onmessage = () => {
    document.write('<h1>Busy replacement frame</h1>');
    document.close();
    console.error('busy replacement console marker');
    const request = new XMLHttpRequest();
    request.open('GET', '/hold-busy-frame', false);
    request.send();
    fetch('/busy-frame-finished');
};
</script>""")
                return
            if self.path == "/hold-busy-frame":
                busy_frame_waiting.set()
                if release_busy_frame.wait(20):
                    self.wfile.write(b"Released")
                return
            if self.path == "/busy-frame-finished":
                busy_frame_finished.set()
                return
            if self.path == "/nested-worker":
                self.wfile.write(b"""<h1>Waiting for workers</h1><button onclick="const w=new Worker('/outer-worker');
                                 w.onmessage=e=>document.querySelector('h1').textContent=e.data">Start nested worker</button>""")
                return
            if self.path == "/worker-fragments":
                self.wfile.write(b"""<h1>Public worker view</h1><button onclick="exerciseWorkers()">Check worker fragments</button>
<script>
async function exerciseWorkers() {
    const workers = [];
    async function spawn(label) {
        const source = "console.log(" + JSON.stringify(label) + ");postMessage('logged')";
        const worker = new Worker(window.URL.createObjectURL(new Blob([source])));
        workers.push(worker);
        await new Promise(resolve => { worker.onmessage = resolve; });
    }
    for (let i = 0; i < 24; i++) {
        history.replaceState({}, '', '#private');
        await spawn('private worker fragment marker');
        history.replaceState({}, '', '#public');
    }
    await spawn('public worker fragment marker');
    await fetch('/worker-fragments-finished');
}
</script>""")
                return
            if self.path == "/worker-fragments-finished":
                worker_fragments_finished.set()
                return
            if self.path == "/worker-fixture-finished":
                worker_fixture_finished.set()
                return
            if self.path == "/worker-history":
                self.wfile.write(b"""<h1>Worker history</h1><button onclick="exerciseHistory()">Exercise worker history</button>
<script>
async function exerciseHistory() {
    const worker = new Worker('/diagnostics-worker');
    worker.postMessage('retained worker history marker');
    await new Promise(resolve => { worker.onmessage = resolve; });
    for (let i = 0; i < 160; i++) history.replaceState({}, '', '#view-' + i);
    history.replaceState({}, '', '/worker-history');
    await fetch('/worker-fixture-finished');
}
</script>""")
                return
            if self.path == "/worker-frame":
                self.wfile.write(f"""<h1>Worker frame</h1><iframe id="child" src="http://localhost:{self.server.server_port}/worker-child"></iframe>
<button onclick="child.contentWindow.postMessage('log', '*')">Remove worker frame</button>
<script>onmessage = () => {{ child.remove(); fetch('/worker-fixture-finished'); }};</script>""".encode())
                return
            if self.path == "/worker-child":
                self.wfile.write(b"""<h1>Worker child</h1><script>
const worker = new Worker('/diagnostics-worker');
onmessage = () => worker.postMessage('detached cross-origin worker marker');
worker.onmessage = () => parent.postMessage('logged', '*');
</script>""")
                return
            if self.path == "/worker-document":
                self.wfile.write(b"""<h1>Worker document</h1><iframe id="child" src="/worker-document-helper"></iframe>
<button onclick="child.contentWindow.replaceParent()">Replace worker document</button>
<test-capture></test-capture>
<script>
customElements.define('test-capture', class extends HTMLElement {
    disconnectedCallback() {
        window.addEventListener('click', event => {
            if (event.isTrusted && event.target.id === 'replacement-action') {
                document.querySelector('h1').textContent = 'Unverified replacement click ran';
                fetch('/named?unverified-replacement-click');
            }
        }, true);
    }
});
const worker = new Worker('/diagnostics-worker');
function logAfterOpen() {
    const privateRoute = location.pathname === '/worker-document-private';
    worker.postMessage('private replacement worker marker');
    worker.onmessage = () => {
        history.replaceState({}, '', '/worker-document');
        document.body.innerHTML = '<h1>Replacement changed URL: ' + privateRoute + '</h1>' +
            '<button id="replacement-action">Change in replacement</button>';
        worker.postMessage('public replacement worker marker');
        worker.onmessage = () => fetch('/worker-fixture-finished');
    };
}
</script>""")
                return
            if self.path == "/worker-document-helper":
                self.wfile.write(b"""<h1>Replacement helper</h1><script>
function replaceParent() {
    setTimeout(() => {
        const target = parent;
        history.replaceState({}, '', '/worker-document-private');
        Document.prototype.open.call(target.document);
        target.document.write('<body></body>');
        target.document.close();
        target.logAfterOpen();
    }, 0);
}
</script>""")
                return
            if self.path == "/release-frame":
                self.wfile.write(b"yes" if release_revoked_frame.is_set() else b"no")
                return
            if self.path == "/cancel-dispatch":
                self.wfile.write(b"<button onclick=\"fetch('/cancel-post',{method:'POST',body:'archive=A'})\">Cancel before dispatch</button>")
                return
            if self.path == "/large":
                self.wfile.write(b"<h1>Pending</h1><button onclick=\"fetch('/blob').then(r=>r.arrayBuffer())"
                                 b".then(data=>{document.querySelector('h1').textContent=data.byteLength;fetch('/download-finished')})\">Fetch large response</button>")
                return
            if self.path == "/download-finished":
                download_finished.set()
                return
            if self.path == "/flip":
                self.wfile.write(flip_mode.encode() if flip.is_set() else b"no")
                return
            if self.path == "/dispatch-phase":
                self.wfile.write(b"yes" if dispatch_after and time.monotonic() >= dispatch_after else b"no")
                return
            if self.path == "/delayed":
                self.wfile.write(b"""<button id="target" onclick="fetch('/named?'+this.textContent)">Delayed click</button>
<div id="overlay" aria-hidden="true" style="position:fixed;inset:0;z-index:5"></div>
<script>let changed=false;setInterval(async()=>{if(!changed&&(await (await fetch('/dispatch-phase')).text())==='yes'){
changed=true;target.textContent='Delete';overlay.remove()}},50)</script>""")
                return
            if self.path == "/flipped":
                flipped.set()
                return
            if self.path.startswith("/clicked?"):
                pixel_clicks.append(self.path)
                return
            if self.path.startswith("/named?"):
                named_clicks.append(self.path)
                return
            if self.path == "/dashboard":
                self.wfile.write(b"<h1>Reports</h1><button onclick=\"history.replaceState({},'', '/dashboard');"
                                 b"document.querySelector('h1').textContent='Private overview'\">Overview</button>")
                return
            if self.path == "/popup-read":
                self.wfile.write(b"<title>Opened details</title><h1>Opened details</h1>")
                return
            self.wfile.write(b"<title>Buddy fixture</title><h1>Ready</h1>"
                             b"<button onclick=\"document.querySelector('h1').textContent='Done'\">Run check</button>"
                             b"<div><span id='ticket'>Ticket A</span> "
                             b"<button id='named' onclick=\"fetch('/named?'+this.textContent)\">Updates</button></div>"
                             b"<button onclick=\"window.open('/redirect-private')\">Redirect popup</button>"
                             b"<button onclick=\"window.open('/popup-read')\">Read popup</button>"
                             b"<button onclick=\"fetch('/named?cancelled')\">Cancel action</button>"
                             b"<button id='changing' onpointerdown=\"this.textContent='Delete'\">Change during click</button>"
                             b"<script>window.addEventListener('click',e=>{if(e.target.id==='changing')"
                             b"fetch('/named?'+e.target.textContent)},true)</script>"
                             b"<button onclick=\"setTimeout(()=>document.querySelector('#graphql').click(),3200)\">"
                             b"Delayed GraphQL</button>"
                             + (f"<button onclick=\"(new Worker('/socket-worker')).postMessage('wss://127.0.0.1:"
                                f"{secure.server_port}/socket')\">Worker socket</button>"
                                f"<button onclick=\"const ws=new WebSocket('wss://127.0.0.1:{secure.server_port}/socket');"
                                "ws.onopen=()=>ws.send('window update')\">Window socket</button>"
                                f"<button onclick=\"fetch('https://127.0.0.1:{secure.server_port}/hold').then(r=>r.text())"
                                ".then(text=>document.querySelector('h1').textContent=text)\">Hold HTTPS response</button>"
                                f"<button onclick=\"fetch('http://127.0.0.1:{outside.server_port}/hold').then(r=>r.text())"
                                ".then(text=>document.querySelector('h1').textContent=text)\">Hold HTTP response</button>").encode()
                             +
                             b"<button id='graphql' onclick=\"fetch('/graphql',{method:'POST',headers:{'Content-Type':'application/json'},"
                             b"body:JSON.stringify({query:'query { updates { title } }'})}).then(r=>r.text())"
                             b".then(text=>document.querySelector('h1').textContent=text)\">Read GraphQL</button>"
                             + (f"<button onclick=\"fetch('http://127.0.0.1:{outside.server_port}/resource-secret')"
                                ".then(r=>r.text()).then(text=>document.querySelector('h1').textContent=text)\">Fetch second origin</button>").encode()
                             +
                             b"<form action='/' method='post'><input type='hidden' name='ticket' value='A'>"
                             b"<button>Submit ticket</button></form>"
                             b"<button id='pixel' style='position:absolute;left:690px;top:120px;width:90px;height:40px' "
                             b"onclick=\"fetch('/clicked?'+this.textContent)\">Play</button>"
                             b"<p id='scroll' style='margin-top:1000px'>Still at top</p>"
                             b"<script>const pixel=document.getElementById('pixel'), named=document.getElementById('named');"
                             b"window.addEventListener('scroll',()=>{if(scrollY>100) document.querySelector('#scroll').textContent='Scrolled to details'});"
                             b"let swapped=false;setInterval(async()=>{const mode=await (await fetch('/flip')).text();if(mode!=='no'&&!swapped)"
                             b"{swapped=true;if(mode==='row'){document.querySelector('#ticket').textContent='Ticket B'}"
                             b"else{pixel.textContent='Delete';named.textContent='Delete'}"
                             b"await new Promise(resolve=>requestAnimationFrame(resolve));fetch('/flipped')}},50);</script>")

        def do_HEAD(self):
            nonlocal download_checks
            if count_download_checks:
                download_checks += 1
                time.sleep(.05)
            if cancel_post_verified.is_set() and not cancel_post_waiting.is_set():
                cancel_post_waiting.set()
                cancel_post_released.wait(8)
            try:
                self.send_response(204 if self.path == "/authorize" else 404)
                self.send_header("Buddy-Automation-Epoch", auth_epoch)
                self.end_headers()
            except (BrokenPipeError, ConnectionResetError):
                if not cancel_post_waiting.is_set():
                    raise

        def do_POST(self):
            nonlocal dispatch_after
            body = self.rfile.read(int(self.headers["Content-Length"]))
            if self.path == "/cancel-post":
                cancelled_posts.append(body)
                self.send_response(204)
                self.end_headers()
                return
            if self.path == "/graphql":
                self.send_response(200)
                self.end_headers()
                self.wfile.write(b"Read-only GraphQL result")
                return
            if self.path == "/":
                submissions.append(body)
                self.send_response(200)
                self.end_headers()
                self.wfile.write(b"<h1>Submitted ticket A</h1>")
                return
            call = json.loads(body)
            permitted = False
            if self.path == "/authorize" and call["provider"] == "playwright":
                if request := call.get("browser_request"):
                    authorized_requests.append(request)
                    if request["method"] == "POST" and request["url"].endswith("/cancel-post"):
                        permitted = call["name"] == "browser_click" and request.get("body") == "archive=A"
                        if permitted:
                            cancel_post_verified.set()
                        self.send_response(204 if permitted else 403)
                        self.send_header("Buddy-Automation-Epoch", auth_epoch)
                        self.end_headers()
                        return
                    if request["method"] == "SEND":
                        permitted = allow_socket and request["body"] in {"worker update", "window update"}
                        self.send_response(204 if permitted else 403)
                        self.send_header("Buddy-Automation-Epoch", auth_epoch)
                        self.end_headers()
                        return
                    permitted = allow_write and request["method"] == "POST" and request["body"] == "ticket=A"
                    if permitted:
                        time.sleep(1.25)
                    permitted |= request["method"] == "POST" and request.get("content_type") == "application/json" and (
                        request["body"] == '{"query":"query { updates { title } }"}')
                elif call["name"] == "browser_navigate":
                    origin = f"http://127.0.0.1:{self.server.server_port}"
                    permitted = call["arguments"].get("url") in {
                        origin + path for path in ("/", "/redirect-outside", "/redirect-private", "/redirect-allowed",
                                                   "/destination", "/popup-read", "/dashboard#reports", "/delayed",
                                                   "/revoked-frame-parent", "/cancel-dispatch", "/large",
                                                   "/replace-document", "/progressive", "/implicit-noop-document",
                                                   "/nested-worker", "/worker-fragments#public", "/worker-history",
                                                   "/worker-frame", "/worker-document", "/worker-document-helper",
                                                   "/implicit-document-console", "/busy-frame-parent")}
                    permitted |= call["arguments"].get("url") in {
                        f"http://localhost:{self.server.server_port}/worker-child",
                        f"http://localhost:{self.server.server_port}/busy-frame-child"}
                    permitted |= revoked_frame_visible and call["arguments"].get("url") == origin + "/revoked-frame"
                    permitted |= bool(lan_origin) and call["arguments"].get("url") in {lan_origin + "/", lan_origin + "/redirect"}
                    permitted |= read_private and call["arguments"].get("url") == origin + "/private"
                    permitted |= read_second_origin and call["arguments"].get("url") in {
                        f"http://127.0.0.1:{outside.server_port}/seed",
                        f"http://127.0.0.1:{outside.server_port}/history",
                        f"https://127.0.0.1:{secure.server_port}/seed",
                    }
                elif call["name"] == "browser_click":
                    element = call["arguments"].get("element")
                    permitted = element in {"Run check", "Updates", "Redirect popup", "Read popup", "Submit ticket", "Overview"}
                    permitted |= element in {"Delayed click", "Cancel action", "Read GraphQL", "Fetch second origin",
                                             "Change during click", "Delayed GraphQL", "Worker socket", "Window socket",
                                             "Hold HTTP response", "Hold HTTPS response", "Cancel before dispatch",
                                             "Fetch large response", "Open replacement details", "Change in replacement",
                                             "Start nested worker", "Check worker fragments", "Exercise worker history",
                                             "Remove worker frame", "Replace worker document", "Block child frame"}
                    if element == "Delayed click":
                        dispatch_after = time.monotonic() + 1.25
                    if element == "Cancel action":
                        cancellation_waiting.set()
                        permitted = cancellation_released.wait(timeout=8)
                    if element == "Updates":
                        flip.set()
                        permitted = flipped.wait(timeout=8)
                elif call["name"] == "browser_mouse_click_xy":
                    permitted = bool(call.get("capture", {}).get("image"))
                    if call["arguments"].get("element") == "Stale Play button" and permitted:
                        flip.set()
                        permitted = flipped.wait(timeout=8)
                elif call["name"] in {"browser_press_key", "browser_mouse_wheel", "browser_tabs"}:
                    permitted = True
            self.send_response(204 if permitted else 403)
            self.send_header("Buddy-Automation-Epoch", auth_epoch)
            self.end_headers()

        def log_message(self, *_):
            pass

    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Fixture)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    try:
        with tempfile.TemporaryDirectory(prefix="buddy-execution-smoke-") as directory:
            frames = pathlib.Path(directory, "frames")
            frames.mkdir(mode=0o700)
            certificate = pathlib.Path(directory, "fixture.crt")
            private_key = pathlib.Path(directory, "fixture.key")
            subprocess.run(["openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:prime256v1",
                            "-nodes", "-subj", "/CN=127.0.0.1", "-days", "1", "-keyout", str(private_key), "-out", str(certificate)],
                           check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            secure = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Forbidden)
            tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
            tls.load_cert_chain(certificate, private_key)
            secure.socket = tls.wrap_socket(secure.socket, server_side=True)
            threading.Thread(target=secure.serve_forever, daemon=True).start()
            with open(pathlib.Path(directory, "provider.log"), "w") as log:
                child = subprocess.Popen([
                    options.worker, "automation-proxy", "-frames", str(frames),
                    "-provider", "playwright", "-guard", f"http://127.0.0.1:{server.server_port}/authorize",
                    "--", options.provider,
                    "--headless", "--isolated", "--no-webmcp", "--ignore-https-errors", "--executable-path", options.browser,
                    "--viewport-size", "800x600", "--device", "Desktop Chrome HiDPI", "--caps", "vision",
                ], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=log, text=True, cwd=directory)
                responses = queue.Queue()
                def read():
                    for line in child.stdout:
                        responses.put(json.loads(line))
                threading.Thread(target=read, daemon=True).start()
                seq = 0
                saw_roots = False

                def send(message):
                    child.stdin.write(json.dumps({"jsonrpc": "2.0", **message}) + "\n")
                    child.stdin.flush()

                def call(method, params, denied=False):
                    nonlocal seq, saw_roots
                    seq += 1
                    send({"id": seq, "method": method, "params": params})
                    while True:
                        message = responses.get(timeout=40)
                        if message.get("method") == "roots/list":
                            saw_roots = True
                            send({"id": message["id"], "result": {"roots": [{"uri": pathlib.Path(directory).as_uri()}]}})
                        elif message.get("id") == seq:
                            assert "error" not in message, message
                            result = message["result"]
                            if denied is not None:
                                assert bool(result.get("isError")) == denied, result
                            return result

                try:
                    call("initialize", {"protocolVersion": "2024-11-05", "capabilities": {"roots": {"listChanged": True}},
                                        "clientInfo": {"name": "buddy-smoke", "version": "1"}})
                    send({"method": "notifications/initialized"})
                    tools = call("tools/list", {})["tools"]
                    assert any(tool["name"] == "browser_navigate" for tool in tools)
                    call("tools/call", {"name": "browser_navigate", "arguments": {
                        "url": f"http://127.0.0.1:{server.server_port}/out-of-scope"}}, denied=True)
                    page = call("tools/call", {"name": "browser_navigate", "arguments": {
                        "url": f"http://127.0.0.1:{server.server_port}/"}})
                    assert "Buddy fixture" in json.dumps(page), page
                    assert saw_roots
                    page = call("tools/call", {"name": "browser_snapshot", "arguments": {}})
                    snapshot = "\n".join(part.get("text", "") for part in page.get("content", []))
                    ref = re.search(r'button "Run check".*?\[ref=([^\]]+)\]', snapshot)
                    assert ref, snapshot
                    click = next(tool for tool in tools if tool["name"] == "browser_click")
                    target_key = "target" if "target" in click["inputSchema"]["properties"] else "ref"
                    call("tools/call", {"name": "browser_click", "arguments": {
                        "element": "Unrelated action", target_key: ref.group(1)}}, denied=True)
                    call("tools/call", {"name": "browser_click", "arguments": {
                        "element": "Run check", target_key: ref.group(1)}})
                    result = call("tools/call", {"name": "browser_snapshot", "arguments": {}})
                    assert "Done" in json.dumps(result), result
                    call("tools/call", {"name": "browser_take_screenshot", "arguments": {"type": "png", "scale": "device"}})
                    frame = json.loads((frames / "frame.json").read_text())
                    assert frame["provider"] == "playwright" and len(frame["data"]) > 100
                    png = base64.b64decode(frame["data"])
                    assert struct.unpack(">II", png[16:24]) == (800, 600), "The HiDPI capture isn't in CSS pixels"
                    mouse = next(tool for tool in tools if tool["name"] == "browser_mouse_click_xy")
                    point = {"x": 730, "y": 140}
                    if "element" in mouse["inputSchema"]["properties"]:
                        point["element"] = "Play button"
                    call("tools/call", {"name": mouse["name"], "arguments": point})
                    assert pixel_clicks == ["/clicked?Play"], pixel_clicks
                    call("tools/call", {"name": "browser_take_screenshot", "arguments": {"type": "png"}})
                    call("tools/call", {"name": mouse["name"], "arguments": dict(point, element="Stale Play button")}, denied=True)
                    assert flipped.is_set() and pixel_clicks == ["/clicked?Play"], pixel_clicks
                    origin = f"http://127.0.0.1:{server.server_port}"

                    def navigate(path, denied=False):
                        return call("tools/call", {"name": "browser_navigate", "arguments": {"url": origin + path}}, denied)

                    def click_params(label):
                        page = call("tools/call", {"name": "browser_snapshot", "arguments": {}})
                        target = re.search(r'button "' + re.escape(label) + r'".*?\[ref=([^\]]+)\]', page["content"][0]["text"])
                        assert target, page
                        return {"name": "browser_click", "arguments": {"element": label, target_key: target.group(1)}}

                    def click_label(label, denied=False):
                        return call("tools/call", click_params(label), denied)

                    flip.clear()
                    flipped.clear()
                    navigate("/")
                    click_label("Updates", denied=True)
                    assert flipped.is_set() and not named_clicks, named_clicks
                    flip.clear()
                    flipped.clear()
                    flip_mode = "row"
                    navigate("/")
                    click_label("Updates", denied=True)
                    assert flipped.is_set() and not named_clicks, named_clicks
                    flip.clear()
                    flipped.clear()
                    result = click_label("Submit ticket", denied=None)
                    assert authorized_requests and authorized_requests[-1]["method"] == "POST" and not submissions, (result, authorized_requests, submissions)
                    assert "Buddy blocked" in json.dumps(result), result
                    navigate("/")
                    click_label("Read GraphQL")
                    result = call("tools/call", {"name": "browser_snapshot", "arguments": {}})
                    assert "Read-only GraphQL result" in json.dumps(result), result
                    allow_write = True
                    click_label("Submit ticket")
                    assert submissions == [b"ticket=A"], submissions
                    allow_write = False
                    navigate("/")
                    call("tools/call", {"name": "browser_press_key", "arguments": {"key": "PageDown"}})
                    result = call("tools/call", {"name": "browser_snapshot", "arguments": {}})
                    assert "Scrolled to details" in json.dumps(result), result
                    navigate("/")
                    call("tools/call", {"name": "browser_mouse_wheel", "arguments": {"deltaY": 550}})
                    result = call("tools/call", {"name": "browser_snapshot", "arguments": {}})
                    assert "Scrolled to details" in json.dumps(result), result
                    click_label("Redirect popup", denied=None)
                    assert not forbidden_hits, forbidden_hits
                    call("tools/call", {"name": "browser_close", "arguments": {}})
                    navigate("/")
                    click_label("Read popup")
                    result = call("tools/call", {"name": "browser_tabs", "arguments": {"action": "list"}})
                    assert "Opened details" in json.dumps(result), result
                    call("tools/call", {"name": "browser_close", "arguments": {}})
                    call("tools/call", {"name": "browser_tabs", "arguments": {"action": "new"}})
                    for path in ("/redirect-outside", "/redirect-private"):
                        call("tools/call", {"name": "browser_navigate", "arguments": {
                            "url": f"http://127.0.0.1:{server.server_port}{path}"}}, denied=True)
                        result = call("tools/call", {"name": "browser_snapshot", "arguments": {}})
                        assert "Secret fixture marker" not in json.dumps(result), result
                    assert not forbidden_hits, forbidden_hits
                    read_private = True
                    navigate("/private")
                    result = call("tools/call", {"name": "browser_snapshot", "arguments": {}})
                    assert "Secret fixture marker" in json.dumps(result), result
                    authorized_hits = forbidden_hits.copy()
                    assert authorized_hits and set(authorized_hits) == {"/private"}, authorized_hits
                    read_private = False
                    auth_epoch = "fixture-2"
                    call("tools/call", {"name": "browser_snapshot", "arguments": {}}, denied=True)
                    navigate("/redirect-private", denied=True)
                    assert forbidden_hits == authorized_hits, (authorized_hits, forbidden_hits)
                    navigate("/dashboard#reports")
                    click_label("Overview", denied=True)
                    result = call("tools/call", {"name": "browser_snapshot", "arguments": {}}, denied=True)
                    assert "Private overview" not in json.dumps(result), result
                    result = call("tools/call", {"name": "browser_navigate", "arguments": {
                        "url": f"http://127.0.0.1:{server.server_port}/redirect-allowed"}})
                    assert "/destination" in json.dumps(result), result
                    navigate("/delayed")
                    result = click_label("Delayed click", denied=True)
                    assert "verified browser click did not complete" in json.dumps(result) and not named_clicks, (result, named_clicks)
                    navigate("/")
                    params = click_params("Cancel action")
                    seq += 1
                    send({"id": seq, "method": "tools/call", "params": params})
                    assert cancellation_waiting.wait(5)
                    send({"method": "notifications/cancelled", "params": {"requestId": seq, "reason": "fixture cancellation"}})
                    time.sleep(0.15)
                    cancellation_released.set()
                    call("tools/call", {"name": "browser_snapshot", "arguments": {}})
                    assert not named_clicks, named_clicks
                    call("tools/call", {"name": "browser_close", "arguments": {}})
                    read_second_origin = True
                    call("tools/call", {"name": "browser_navigate", "arguments": {"url": f"http://127.0.0.1:{outside.server_port}/seed"}})
                    call("tools/call", {"name": "browser_tabs", "arguments": {"action": "new"}})
                    navigate("/")
                    read_second_origin = False
                    auth_epoch = "fixture-3"
                    result = call("tools/call", {"name": "browser_snapshot", "arguments": {}})
                    assert "Buddy fixture" in json.dumps(result), result
                    result = call("tools/call", {"name": "browser_tabs", "arguments": {"action": "list"}})
                    assert "Outside automation scope" in json.dumps(result) and "/seed" not in json.dumps(result), result
                    click_label("Fetch second origin")
                    assert "/resource-secret" not in forbidden_hits, forbidden_hits
                    call("tools/call", {"name": "browser_tabs", "arguments": {"action": "close", "index": 0}})
                    call("tools/call", {"name": "browser_close", "arguments": {}})
                    navigate("/")
                    click_label("Change during click", denied=True)
                    assert not named_clicks, named_clicks
                    click_label("Delayed GraphQL")
                    time.sleep(3.5)
                    page = call("tools/call", {"name": "browser_snapshot", "arguments": {}})
                    assert "Read-only GraphQL result" in json.dumps(page), page

                    read_second_origin = True
                    outside_origin = f"http://127.0.0.1:{outside.server_port}"
                    secure_origin = f"https://127.0.0.1:{secure.server_port}"
                    call("tools/call", {"name": "browser_navigate", "arguments": {"url": outside_origin + "/history"}})
                    diagnostics = [("browser_network_requests", {"all": True, "static": True}, "private-network-marker"),
                                   ("browser_console_messages", {"all": True}, "private console marker")]
                    for tool, arguments, marker in diagnostics:
                        assert marker in json.dumps(call("tools/call", {"name": tool, "arguments": arguments})), marker
                    navigate("/")
                    read_second_origin = False
                    auth_epoch = "fixture-4"
                    for tool, arguments, marker in diagnostics:
                        assert marker not in json.dumps(call("tools/call", {"name": tool, "arguments": arguments})), marker

                    read_second_origin = True
                    call("tools/call", {"name": "browser_navigate", "arguments": {"url": secure_origin + "/seed"}})
                    navigate("/")
                    result = click_label("Worker socket")
                    assert "WebSocket message" in json.dumps(result) and not socket_messages, (result, socket_messages)
                    allow_socket = True
                    click_label("Worker socket")
                    click_label("Window socket")
                    assert socket_messages == [b"worker update", b"window update"], socket_messages

                    for index, (destination, label) in enumerate(((outside_origin, "Hold HTTP response"),
                                                                 (secure_origin, "Hold HTTPS response")), start=5):
                        read_second_origin = True
                        call("tools/call", {"name": "browser_navigate", "arguments": {"url": destination + "/seed"}})
                        navigate("/")
                        held_response.clear()
                        release_response.clear()
                        params = click_params(label)
                        seq += 1
                        send({"id": seq, "method": "tools/call", "params": params})
                        assert held_response.wait(8), label
                        read_second_origin = False
                        auth_epoch = f"fixture-{index}"
                        release_response.set()
                        page = call("tools/call", {"name": "browser_snapshot", "arguments": {}})
                        assert "revoked response marker" not in json.dumps(page), page
                    if lan:
                        call("tools/call", {"name": "browser_navigate", "arguments": {"url": lan_origin + "/"}})
                        assert "LAN asset loaded" in json.dumps(call("tools/call", {"name": "browser_snapshot", "arguments": {}}))
                        hits = forbidden_hits.count("/private")
                        call("tools/call", {"name": "browser_navigate", "arguments": {"url": lan_origin + "/redirect"}}, denied=True)
                        assert forbidden_hits.count("/private") == hits, forbidden_hits

                    navigate("/revoked-frame-parent")
                    console = {"name": "browser_console_messages", "arguments": {"all": True}}
                    initial_console = json.dumps(call("tools/call", console))
                    assert "initial frame console marker" in initial_console, initial_console
                    assert "initial worker console marker" in initial_console, initial_console
                    revoked_frame_visible = False
                    auth_epoch = "fixture-7"
                    tabs = call("tools/call", {"name": "browser_tabs", "arguments": {"action": "list"}})
                    assert "Outside automation scope" in json.dumps(tabs), tabs
                    release_revoked_frame.set()
                    time.sleep(.6)
                    page = call("tools/call", {"name": "browser_snapshot", "arguments": {}})
                    assert "Allowed parent" in json.dumps(page) and "Private frame" not in json.dumps(page), page
                    later_console = json.dumps(call("tools/call", console))
                    assert "revoked frame console marker" not in later_console, later_console
                    assert "revoked worker console marker" not in later_console, later_console

                    navigate("/cancel-dispatch")
                    params = click_params("Cancel before dispatch")
                    seq += 1
                    send({"id": seq, "method": "tools/call", "params": params})
                    assert cancel_post_waiting.wait(8)
                    send({"method": "notifications/cancelled", "params": {"requestId": seq, "reason": "cancel before dispatch"}})
                    time.sleep(.2)
                    cancel_post_released.set()
                    call("tools/call", {"name": "browser_snapshot", "arguments": {}})
                    assert not cancelled_posts, cancelled_posts

                    call("tools/call", {"name": "browser_close", "arguments": {}})
                    navigate("/large")
                    count_download_checks = True
                    click_label("Fetch large response")
                    assert download_finished.wait(10)
                    count_download_checks = False
                    assert download_checks < 30, download_checks
                    navigate("/progressive")
                    assert progressive_continued == [True], progressive_continued

                    navigate("/replace-document")
                    time.sleep(.2)
                    hits = len(named_clicks)
                    click_label("Open replacement details")
                    assert named_clicks[hits:] == ["/named?Open%20replacement%20details"], named_clicks
                    click_label("Change in replacement", denied=True)
                    assert named_clicks[hits:] == ["/named?Open%20replacement%20details"], named_clicks
                    navigate("/implicit-noop-document")
                    time.sleep(.2)
                    click_label("Change in replacement", denied=True)
                    assert named_clicks[hits:] == ["/named?Open%20replacement%20details"], named_clicks

                    navigate("/implicit-document-console")
                    assert worker_fixture_finished.wait(8)
                    worker_fixture_finished.clear()
                    logs = json.dumps(call("tools/call", console))
                    assert "implicit replacement page error" in logs, logs

                    navigate("/nested-worker")
                    click_label("Start nested worker")
                    logs = json.dumps(call("tools/call", console))
                    assert "outer worker console marker" in logs and "nested worker console marker" in logs, logs

                    navigate("/worker-fragments#public")
                    click_label("Check worker fragments", denied=None)
                    assert worker_fragments_finished.wait(8)
                    logs = json.dumps(call("tools/call", console))
                    assert "public worker fragment marker" in logs, logs
                    assert "private worker fragment marker" not in logs, logs

                    navigate("/worker-history")
                    click_label("Exercise worker history", denied=None)
                    assert worker_fixture_finished.wait(8)
                    worker_fixture_finished.clear()
                    logs = json.dumps(call("tools/call", console))
                    assert "retained worker history marker" in logs, logs

                    call("tools/call", {"name": "browser_navigate", "arguments": {
                        "url": f"http://localhost:{server.server_port}/worker-child"}})
                    navigate("/worker-frame")
                    click_label("Remove worker frame")
                    assert worker_fixture_finished.wait(8)
                    worker_fixture_finished.clear()
                    logs = json.dumps(call("tools/call", console))
                    assert "detached cross-origin worker marker" in logs, logs

                    navigate("/worker-document")
                    click_label("Replace worker document", denied=None)
                    assert worker_fixture_finished.wait(8)
                    page = call("tools/call", {"name": "browser_snapshot", "arguments": {}})
                    assert "Replacement changed URL: true" in json.dumps(page), page
                    logs = json.dumps(call("tools/call", console))
                    assert "public replacement worker marker" in logs, logs
                    assert "private replacement worker marker" not in logs, logs
                    hits = len(named_clicks)
                    click_label("Change in replacement", denied=True)
                    page = call("tools/call", {"name": "browser_snapshot", "arguments": {}})
                    assert "Replacement changed URL: true" in json.dumps(page), page
                    assert named_clicks[hits:] == [], named_clicks

                    call("tools/call", {"name": "browser_navigate", "arguments": {
                        "url": f"http://localhost:{server.server_port}/busy-frame-child"}})
                    navigate("/busy-frame-parent")
                    click_label("Block child frame")
                    assert busy_frame_waiting.wait(8)
                    started = time.monotonic()
                    logs = json.dumps(call("tools/call", console))
                    assert time.monotonic() - started < 8, "A busy child blocked the parent diagnostics"
                    assert "responsive parent console marker" in logs, logs
                    assert "busy replacement console marker" not in logs, logs
                    release_busy_frame.set()
                    assert busy_frame_finished.wait(8)
                    logs = json.dumps(call("tools/call", console))
                    assert "responsive parent console marker" in logs, logs
                    assert "busy replacement console marker" in logs, logs

                    print(f"PASS: {len(tools)} tools. Verified capture-phase click dispatch, cancellation before dispatch, "
                          "worker and window WebSockets, delayed GraphQL, frame diagnostics, HTTP and HTTPS revocation, "
                          "worker console attribution and retention, responsive diagnostics with a busy frame, "
                          "progressive responses, replacement documents, redirects and MCP roots.")
                finally:
                    child.stdin.close()
                    try:
                        child.wait(timeout=10)
                    except subprocess.TimeoutExpired:
                        child.kill()
                        child.wait()
                    release_response.set()
                    release_busy_frame.set()
                    cancel_post_released.set()
                    secure.shutdown()
                    secure.server_close()
    finally:
        server.shutdown()
        server.server_close()
        outside.shutdown()
        outside.server_close()
        if lan:
            lan.shutdown()
            lan.server_close()


if __name__ == "__main__":
    main()
