#!/usr/bin/env python3
"""Exercise Buddy chat, delegated work, and groups in the native desktop."""
import argparse
import base64
import hmac
import json
import os
import signal
from pathlib import Path
import socket
import sqlite3
import ssl
import struct
import zlib
import subprocess
import tempfile
import time
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
RUN = ROOT / "script" / "build_and_run.sh"


def wait_for(label, fn, timeout=45):
    end = time.monotonic() + timeout
    last = None
    while time.monotonic() < end:
        try:
            if result := fn():
                return result
        except (OSError, RuntimeError, ValueError) as exc:
            last = exc
        time.sleep(0.2)
    raise RuntimeError(f"Timed out waiting for {label}: {last}")


def run(directory, screenshot, activity_screenshot, reply_screenshot, routine_screenshot, card_screenshot,
        connections_only=False, buds_only=False, workbench_only=False):
    repo = directory / "sample repo "
    state = repo / "data"
    dist = repo / "dist"
    repo.mkdir()
    subprocess.run(["git", "init", "--quiet", str(repo)], check=True)
    (repo / "show-result.sh").write_text(
        "#!/bin/sh\n"
        "i=0\nwhile [ ! -f result.txt ] && [ \"$i\" -lt 200 ]; do\n"
        "  sleep .2\n  i=$((i + 1))\ndone\n"
        "cat result.txt\n"
    )
    with socket.socket() as probe:
        probe.bind(("127.0.0.1", 0))
        port = str(probe.getsockname()[1])
    sock_path = directory / "ui.sock"
    env = {
        **os.environ,
        "BUDDY_DATA_DIR": str(state),
        "BUDDY_WORKSPACES_DIR": str(directory / "workspaces"),
        "BUDDY_DIST_DIR": str(dist),
        "BUDDY_PORT": port,
        "BUDDY_AUTOMATION_SOCKET": str(sock_path),
        "BUDDY_INITIAL_REPO": str(repo),
        "BUDDY_VOICE_API_KEY": "",
    }

    def ui(method, **params):
        with socket.socket(socket.AF_UNIX) as sock:
            sock.settimeout(4)
            sock.connect(str(sock_path))
            request = dict(id=1, method=method, params=params)
            sock.sendall(json.dumps(request).encode() + b"\n")
            with sock.makefile("rb") as stream:
                result = json.loads(stream.readline())
            if "error" in result:
                raise RuntimeError(result["error"])
            return result["result"]

    def tree_has(text):
        return any(text in n.get(field, "") for n in ui("tree")["nodes"]
                   for field in ("label", "description", "value", "placeholder"))

    def node(key):
        return next((n for n in ui("tree")["nodes"] if n.get("key") == key), None)

    def press(key):
        ui("act", key=key, action="press")

    def navigate(key):
        press("nav-menu")
        wait_for(key, lambda: node(key))
        press(key)

    def set_value(key, value):
        ui("act", key=key, action="setValue", text=value)

    def reveal_setting(key):
        if not node(key):
            bounds = wait_for("settings position", lambda: node("agent-name"))["bounds"]
            ui("input", events=[dict(type="wheel", x=bounds["x"] + bounds["width"] / 2,
                                     y=bounds["y"] + bounds["height"] / 2, dy=330, precise=True)])
        return wait_for(key, lambda: node(key))

    def capture(path):
        if path:
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(base64.b64decode(ui("capture")["png"]))

    def api(path, data=None):
        token = (state / "server.token").read_text().strip()
        request = urllib.request.Request(
            f"https://127.0.0.1:{port}/v1/{path}",
            headers={"Authorization": f"Bearer {token}", "Content-Type": "application/json"},
            data=json.dumps(data).encode() if data is not None else None,
        )
        tls = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
        tls.load_verify_locations(cafile=str(state / "server.crt"))
        with urllib.request.urlopen(request, timeout=3, context=tls) as response:
            return json.load(response)

    def chat(chat_id="conv_buddy"):
        return api(f"conversations/{chat_id}")

    def reply_matching(predicate, chat_id="conv_buddy"):
        return next((m for m in chat(chat_id)["messages"] if m["role"] == "assistant" and predicate(m)), None)

    app_path = dist / "Buddy.app" / "Contents" / "MacOS" / "BuddyDesktop"
    def app_pid():
        output = subprocess.check_output(["ps", "-axo", "pid=,comm="], text=True)
        for line in output.splitlines():
            pid, *command = line.strip().split(maxsplit=1)
            if command == [str(app_path)]:
                return int(pid)
        return None

    def launch():
        subprocess.run([RUN, "--verify"], env=env, check=True)
        wait_for("connected desktop", lambda: tree_has("Server connected"))

    def close():
        try:
            ui("menu", path=["File", "Close Window"])
        except (BrokenPipeError, ConnectionResetError, ValueError):
            pass  # The menu action can quit before the socket writes its reply.
        try:
            wait_for("desktop window to close", lambda: not app_pid(), 2)
        except RuntimeError:
            if pid := app_pid():
                os.kill(pid, signal.SIGTERM)
            wait_for("desktop process exit", lambda: not app_pid(), 8)

    def send(text, key="send"):
        set_value("chat-input", text)
        wait_for(f"enabled {key}", lambda: any(
            n.get("key") == key and (key != "send" or n.get("label") == "Send")
            and "disabled" not in n.get("states", [])
            for n in ui("tree")["nodes"]
        ))
        press(key)

    def composer_keys(*keys):
        bounds = wait_for("composer", lambda: node("chat-input"))["bounds"]
        ui("input", events=[dict(type="click", x=bounds["x"] + bounds["width"] / 2,
                                y=bounds["y"] + bounds["height"] / 2)] +
                           [dict(type="key", key=key) for key in keys])

    def verify_new_address():
        navigate("nav-connections")
        wait_for("move action", lambda: node("move-host"))
        press("move-host")
        wait_for("new address field", lambda: node("moved-address"))
        set_value("moved-address", f"https://localhost:{port}")
        wait_for("enabled address verification", lambda: (n := node("use-moved-address")) and
                 "disabled" not in n.get("states", []))
        press("use-moved-address")
        try:
            wait_for("verified address and reopen action", lambda: node("apply-host"), 12)
        except RuntimeError as err:
            state_labels = [n.get("label", "") for n in ui("tree")["nodes"] if
                            n.get("key") in ("use-moved-address", "apply-host") or
                            any(hint in n.get("label", "") for hint in
                                ("Couldn't verify", "Export a move backup", "Reopen Buddy"))]
            raise RuntimeError(f"{err}. Server move controls: {state_labels}") from err
        if json.loads((state / "desktop-connection.json").read_text())["server"] != f"https://localhost:{port}":
            raise RuntimeError("Buddy did not save the verified address")

    def choose_menu_item(label):
        item = wait_for(label, lambda: next((n for n in ui("tree")["nodes"] if n.get("label") == label and n.get("bounds")), None))
        rect = item["bounds"]
        ui("input", events=[dict(type="click", x=rect["x"] + rect["width"] / 2, y=rect["y"] + rect["height"] / 2)])

    def verify_buds():
        launch()
        forge = api("agents", {"name": "Forge", "role": "Coding specialist"})
        navigate("nav-agent")
        wait_for("Bud directory", lambda: node("edit-bud-agt_buddy") and node("edit-bud-" + forge["id"]))
        if node("agent-name"):
            raise RuntimeError("Bud settings opened directly to an editor")
        press("edit-bud-" + forge["id"])
        wait_for("Forge’s profile editor", lambda: (n := node("agent-name")) and n.get("value") == "Forge")
        press("avatar-color-teal")
        press("avatar-shape-cloud")
        wait_for("selected Cloud shape", lambda: "checked" in (node("avatar-shape-cloud") or {}).get("states", []))
        capture(screenshot)
        press("agent-save")
        wait_for("return to Bud directory", lambda: node("edit-bud-" + forge["id"]))
        avatar = chat(forge["conversation_id"])["agent"]["avatar"]
        if avatar["color"] != "teal" or avatar["shape"] != "cloud":
            raise RuntimeError(f"Buddy did not save the new look: {avatar}")
        press("primary-bud-" + forge["id"])
        wait_for("Forge set as primary", lambda: api("workspace")["primary_agent_id"] == forge["id"])
        close()
        launch()
        wait_for("open on Primary Bud", lambda: tree_has("Message Forge"))
        press("New group chat")
        wait_for("group member options", lambda: node("member-" + forge["id"]))
        set_value("group-name", "Workshop")
        for member in ("member-agt_buddy", "member-" + forge["id"]):
            press(member)
            wait_for(f"checked {member}", lambda: "checked" in (node(member) or {}).get("states", []))
        wait_for("enabled group save", lambda: (n := node("form-save")) and "disabled" not in n.get("states", []))
        press("form-save")
        group = wait_for("created group", lambda: next(iter(api("workspace").get("groups") or []), None))
        wait_for("default group lead and role", lambda: tree_has("Lead: Forge · Coding specialist"))
        press("group-lead")
        choose_menu_item("Buddy · Personal companion")
        wait_for("changed group lead", lambda: chat(group["id"])["agent"]["id"] == "agt_buddy")
        wait_for("updated group lead in the header", lambda: tree_has("Lead: Buddy · Personal companion"))
        print("Native Bud settings, avatar save, primary on launch, and group lead selection worked.", flush=True)

    def verify_workbench():
        tools = directory / "fixture-bin"
        tools.mkdir()
        codex = tools / "codex"
        codex.write_text("#!/bin/sh\nexit 1\n")
        codex.chmod(0o700)
        env["PATH"] = f"{tools}{os.pathsep}{env['PATH']}"
        env["BUDDY_VERSION"] = "0.1.0-beta.1+smoke"
        env["BUDDY_BUILD_NUMBER"] = "37"
        hook_secret = "desktop-smoke-hook"
        state.mkdir(mode=0o700)
        automation_settings = state / "automations.json"
        automation_settings.write_text(json.dumps({"github_webhook_secret": hook_secret,
                                                    "snapshot_dir": str(directory / "missing-snapshots")}))
        automation_settings.chmod(0o600)
        launch()
        wait_for("version beside Buddy", lambda: (node("buddy-build") or {}).get("label") == "0.1.0-beta.1")
        navigate("nav-preferences")
        wait_for("theme settings", lambda: node("theme-dark"))
        wait_for("full build information", lambda: tree_has("0.1.0-beta.1+smoke") and tree_has("Beta · Build 37"))
        capture(reply_screenshot)
        press("theme-dark")
        wait_for("saved dark theme", lambda: (state / "desktop-theme").read_text().strip() == "dark")
        wait_for("enabled voice language", lambda: (n := node("voice-language")) and "disabled" not in n.get("states", []))
        press("voice-language")
        wait_for("language search", lambda: node("voice-language-search"))
        set_value("voice-language-search", "Swedish")
        wait_for("Swedish language option", lambda: node("language-sv"))
        press("language-sv")
        wait_for("separate call language", lambda: api("preferences/languages") == {"chat": "", "voice": "sv"})
        wait_for("saved language shown", lambda: (node("voice-language") or {}).get("description") == "Swedish")
        capture(screenshot)

        workspace = Path(chat()["agent"]["workspace"])
        (workspace / "notes").mkdir()
        note = workspace / "notes" / "plan.md"
        note.write_bytes(b"# Original heading\r\n\nOriginal paragraph\n")
        (workspace / "notes" / "buddy.png").write_bytes((ROOT / "cmd/buddy/assets/Buddy.png").read_bytes())
        press("chat-conv_buddy")
        wait_for("workspace browser", lambda: (n := node("open-workspace")) and "disabled" not in n.get("states", []))
        if node("workspace-tabs"):
            raise RuntimeError("A chat without an open file should have no tab strip")
        windows = {w["id"] for w in ui("windows")["windows"]}
        press("open-workspace")
        wait_for("workspace tab in Buddy’s main window", lambda: node("workspace-entry-notes"))
        if {w["id"] for w in ui("windows")["windows"]} != windows or node("chat-input"):
            raise RuntimeError("Opening workspace files should switch the current conversation to Files")

        def conversation_tab(label):
            tab = wait_for(label + " tab", lambda: next((n for n in ui("tree")["nodes"]
                           if n.get("label") == label and n.get("role") == "tab"), None))
            ui("act", id=tab["id"], action="press")

        def file_press(key):
            wait_for(key, lambda: node(key))
            press(key)

        file_press("workspace-entry-notes")
        file_press("workspace-entry-plan.md")
        wait_for("rendered file heading", lambda: tree_has("Original heading"))
        file_press("file-edit-mode")
        field = wait_for("file editor", lambda: next((n for n in ui("tree")["nodes"] if n.get("label") == "File editor"), None))
        ui("act", id=field["id"], action="setValue", text="# Saved heading\n\nOriginal paragraph\n")
        conversation_tab("Chat")
        wait_for("return to chat", lambda: node("chat-input") and not node("file-save"))
        other = api("agents", {"name": "Files fixture"})
        other_chat = "chat-" + other["conversation_id"]
        wait_for("another Bud’s chat", lambda: node(other_chat))
        press(other_chat)
        wait_for("another Bud’s workspace", lambda: (n := node("open-workspace")) and "disabled" not in n.get("states", []))
        press("open-workspace")
        wait_for("separate empty workspace", lambda: tree_has("This folder is empty."))
        press("chat-conv_buddy")
        wait_for("original chat", lambda: node("chat-input"))
        conversation_tab("plan.md")
        wait_for("retained unsaved file draft", lambda: tree_has("Saved heading") and tree_has("Unsaved changes"))
        wait_for("enabled file save", lambda: (n := node("file-save")) and "disabled" not in n.get("states", []))
        file_press("file-save")
        wait_for("saved workspace file with mixed line endings",
                 lambda: note.read_bytes() == b"# Saved heading\r\n\nOriginal paragraph\n")
        wait_for("saved status", lambda: tree_has("Saved"))
        file_press("file-preview-mode")
        wait_for("updated file preview", lambda: tree_has("Saved heading"))
        file_press("file-up")
        file_press("workspace-entry-buddy.png")
        wait_for("native image preview", lambda: node("file-image"))
        conversation_tab("Chat")
        wait_for("image preview hidden in Chat", lambda: node("chat-input") and not node("file-image"))
        file_press("close-file-tab")
        wait_for("chat without the file tab", lambda: not node("close-file-tab"))

        navigate("nav-autonomy")
        wait_for("Autonomy tabs", lambda: node("autonomy-watches"))
        press("autonomy-watches")
        wait_for("watch creation button", lambda: node("new-watch"))
        wait_for("webhook-only catch-up warning", lambda: tree_has("Add an Actions read token for catch-up"))
        press("new-watch")
        wait_for("watch name input", lambda: node("watch-name"))
        set_value("watch-name", "CI watch")
        set_value("watch-scope", "example/project")
        press("watch-mode-digest")
        wait_for("enabled watch save", lambda: (n := node("watch-save")) and "disabled" not in n.get("states", []))
        press("watch-save")
        try:
            watch = wait_for("saved CI watch", lambda: next(iter(api("watches") or []), None), 10)
        except RuntimeError as err:
            raise RuntimeError([n for n in ui("tree")["nodes"] if str(n.get("key", "")).startswith("watch-")
                                or any(word in n.get("label", "") for word in ("watch", "scope", "repository"))]) from err
        body = json.dumps({"action": "completed", "repository": {"full_name": "example/project"},
                           "workflow_run": {"id": 37, "run_attempt": 1, "name": "Unit tests", "conclusion": "failure"}}).encode()
        signature = hmac.digest(hook_secret.encode(), body, "sha256").hex()
        tls = ssl.create_default_context(cafile=str(state / "server.crt"))
        for _ in range(2):
            event = urllib.request.Request(f"https://127.0.0.1:{port}/hooks/github", data=body, headers={
                "X-GitHub-Event": "workflow_run", "X-Hub-Signature-256": "sha256=" + signature,
                "Content-Type": "application/json",
            })
            with urllib.request.urlopen(event, timeout=3, context=tls) as response:
                if response.status != 204:
                    raise RuntimeError(f"Unexpected webhook status: {response.status}")
        inbox = api("inbox")
        if len(inbox) != 1 or inbox[0]["state"] != "digest":
            raise RuntimeError(f"Unexpected watch inbox: {inbox}")
        press("autonomy-inbox")
        wait_for("rendered CI failure", lambda: tree_has("Unit tests failed"))
        press("read-" + inbox[0]["id"])
        wait_for("marked CI failure read", lambda: api("inbox")[0].get("seen"))
        press("autonomy-watches")
        toggle = "watch-toggle-" + watch["id"]
        wait_for("watch pause button", lambda: node(toggle))
        press(toggle)
        wait_for("paused watch", lambda: not api("watches")[0]["enabled"])
        archive = "watch-archive-" + watch["id"]
        wait_for("watch archive button", lambda: node(archive))
        press(archive)
        wait_for("archived watch", lambda: not api("watches"))
        if len(api("inbox")) != 1:
            raise RuntimeError("Archiving a watch removed its inbox history")
        press("autonomy-memory")
        wait_for("memory editor", lambda: node("memory-note"))
        set_value("memory-note", "Prefer focused Go examples.")
        press("memory-save")
        wait_for("persisted Bud memory", lambda: any(n["note"] == "Prefer focused Go examples."
                 for n in api("agents/agt_buddy/memories") or []))
        press("autonomy-health")
        wait_for("rendered server health", lambda: tree_has("Home server"))
        wait_for("backup failure with desktop still connected", lambda: tree_has("Database snapshot failed"))
        print("Native watch setup, signed CI event, inbox, archive, memory, and degraded backup health worked.", flush=True)

        job, turn, active = "fixture-job", "fixture-turn", "fixture-active"
        waiting, question = "fixture-waiting", "fixture-question"
        patch = "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new"
        now = time.time_ns() // 1000
        with sqlite3.connect(state / "buddy.db") as db:
            for task, kind, origin, title, brief in [
                (job, "background_work", "handoff", "Inspect the build", "Read the build log."),
                (turn, "agent_turn", "chat", "", ""),
                (active, "background_work", "handoff", "Check dependencies", "Review versions."),
                (waiting, "background_work", "handoff", "Review the repo", "Review the requested repository."),
                (question, "work_input", "work_input", "Review the repo", ""),
                ("older-check", "background_work", "handoff", "Older check", "Read the earlier logs."),
            ]:
                db.execute("INSERT INTO tasks VALUES (?,?,?,'','null',60,'succeeded','Finished.',?,?,?)",
                           (task, task, kind, now, now, now))
                db.execute("INSERT INTO chat_turns VALUES (?,'conv_buddy','Buddy','',0)", (task,))
                db.execute("INSERT INTO turn_details VALUES (?,'agt_buddy','Personal companion',?,?,?,NULL,NULL)",
                           (task, origin, title, brief))
            db.execute("UPDATE tasks SET status='running',finished=NULL WHERE id=?", (active,))
            db.execute("UPDATE tasks SET status='needs_input',finished=NULL WHERE id IN (?,?)", (waiting, question))
            db.execute("INSERT INTO work_inputs(task_id,job_id) VALUES (?,?)", (question, waiting))
            db.execute("INSERT INTO work_questions VALUES (?,?)", (question, waiting))
            db.executemany("INSERT INTO events(task_id,at,stream,text) VALUES (?,?,'commentary',?)", [
                (job, now, "**Checked** the"),
                (job, now, "**Checked** the build log.\n\n```sh\ngo test ./...\n```"),
                (job, now, "**Build"),
                (job, now, "**Build passed.**"),
                (active, now, "Reading the dependency manifest."),
            ])
            db.executemany("INSERT INTO messages(task_id,role,text,created) VALUES (?,?,?,?)", [
                (job, "assistant", "**Build passed.**\n\n```go\nfunc main() {}\n```", now),
                (turn, "user", patch, now),
                (turn, "assistant", "Here are two ways forward.", now),
                (question, "assistant", "Which repository should I review?", now),
            ])
            db.execute("INSERT INTO choice_cards VALUES (?,?,?,NULL,NULL)", (turn, "How should I proceed?", '["Run tests","Review logs"]'))
            db.execute("INSERT INTO choice_card_details VALUES (?,?,?)",
                       (turn, "**One check** should be enough.", '["Run the Go suite.","Read the previous output."]'))

        navigate("nav-work")
        wait_for("finished work row", lambda: node("job-" + job))
        press("job-" + job)
        wait_for("job result, code, and history", lambda: all(tree_has(label) for label in
                 ("Build passed.", "Read the build log.", "Checked the build log.", "func main() {}", "go test ./...")))
        if tree_has("**Checked**") or tree_has("**Build"):
            raise RuntimeError("Work exposed raw Markdown or a duplicate answer")
        capture(activity_screenshot)
        press("ask-work")
        wait_for("linked job in draft", lambda: tree_has("About Inspect the build") and tree_has("What’s the latest on this job?"))
        wait_for("formatted diff and choice card", lambda: all(tree_has(label) for label in
                 ("Git diff", "main.go", "How should I proceed?", "Run the Go suite.")))
        wait_for("active job in workspace", lambda: node("open-work-" + active) and tree_has("In progress"))
        if tree_has("Reading the dependency manifest."):
            raise RuntimeError("The workspace sidebar exposed a background update")
        wait_for("bounded work preview", lambda: node("more-chat-jobs"))
        if len([n for n in ui("tree")["nodes"] if str(n.get("key", "")).startswith("open-work-")]) != 3:
            raise RuntimeError("The workspace sidebar did not cap its work preview")
        capture(card_screenshot)
        press("send")
        wait_for("job reference persisted with message", lambda: any(
            m.get("job_id") == job and m.get("job_title") == "Inspect the build" for m in chat()["messages"]))
        press("choice-fixture-turn-0")
        wait_for("selected choice", lambda: any(m.get("card", {}).get("choice") == 0 for m in chat()["messages"]))
        wait_for("clickable job reference in transcript", lambda: node("about-job"))
        press("about-job")
        wait_for("job reopened from chat", lambda: tree_has("Read the build log."))
        wait_for("waiting job in Work", lambda: node("job-" + waiting))
        press("job-" + waiting)
        wait_for("job question in Work", lambda: tree_has("Which repository should I review?"))
        press("ask-work")
        wait_for("composer linked to the waiting job", lambda: tree_has("Replying to Review the repo"))
        send("Use the API repository.")
        wait_for("answer sent back to the job", lambda: any(
            m["task_id"] == question and m["role"] == "user" and m.get("job_id") == waiting
            for m in chat()["messages"]))
        wait_for("job reply draft cleared", lambda: not tree_has("Replying to Review the repo"))
        press("work-overview")
        wait_for("chat-scoped work overview", lambda: node("all-work"))
        print("Native build info, workspace file editing, dark theme, language settings, Markdown, diffs, choice cards, and linked job details worked.", flush=True)

    worker = None
    try:
        if workbench_only:
            verify_workbench()
            return
        if buds_only:
            verify_buds()
            return
        state.mkdir(mode=0o700)
        saved_connection = state / "desktop-connection.json"
        saved_connection.write_text("{invalid connection")
        saved_connection.chmod(0o600)
        subprocess.run([RUN, "--verify"], env=env, check=True)
        wait_for("local fallback for invalid connection", lambda: node("local-host"))
        press("local-host")
        wait_for("invalid connection cleared", lambda: not saved_connection.exists())
        close()
        launch()
        original = chat()["agent"]
        if not Path(original["workspace"]).is_dir() or not original["allow_edits"]:
            raise RuntimeError("Buddy did not get a writable default workspace")
        wait_for("native call button", lambda: (n := node("start-voice")) and "disabled" not in n.get("states", []))
        press("start-voice")
        wait_for("call setup with no voice key", lambda: node("voice-api-key"))
        set_value("voice-api-key", "sk-buddy-voice-smoke-placeholder")
        wait_for("enabled voice Save key", lambda: (n := node("save-voice-key")) and "disabled" not in n.get("states", []))
        press("save-voice-key")
        wait_for("voice setup collapsed", lambda: not node("voice-api-key"))
        press("manage-voice")
        wait_for("voice key saved", lambda: (n := node("remove-voice-key")) and "disabled" not in n.get("states", []))
        press("remove-voice-key")
        wait_for("voice key removed", lambda: not node("remove-voice-key"))
        press("manage-voice")
        print("The call button opened Live voice setup, and the separate key could be saved and removed.", flush=True)

        navigate("nav-connections")
        wait_for("device pairing action", lambda: node("pair-device"))
        press("pair-device")
        wait_for("guided pairing", lambda: node("device-server"))
        press("pair-device")
        press("move-host")
        wait_for("guided server move", lambda: node("moved-address"))
        press("move-host")
        wait_for("Codex runtime row", lambda: node("manage-codex"))
        if node("codex-api-key"):
            raise RuntimeError("OpenAI API key field is visible without opening runtime settings")
        press("manage-codex")
        wait_for("API key input", lambda: node("codex-api-key"))
        set_value("codex-api-key", "sk-buddy-smoke-placeholder")
        wait_for("enabled Save key", lambda: (n := node("codex-api-key-save")) and "disabled" not in n.get("states", []))
        press("codex-api-key-save")
        wait_for("saved key state", lambda: node("codex-api-key-remove"))
        press("codex-api-key-remove")
        wait_for("CLI sign-in restored", lambda: not node("codex-api-key-remove"))

        if connections_only:
            verify_new_address()
            previous_pid = app_pid()
            press("apply-host")
            wait_for("restarted desktop process", lambda: (pid := app_pid()) and pid != previous_pid)
            wait_for("reopened desktop", lambda: tree_has("Server connected"))
            navigate("nav-connections")
            wait_for("hosted connection after reopen", lambda: tree_has("Hosted Buddy server"))
            print("Buddy verified the new address, reopened, and authenticated at the new origin.", flush=True)
            return

        navigate("nav-agent")
        wait_for("Bud settings directory", lambda: node("edit-bud-agt_buddy"))
        press("edit-bud-agt_buddy")
        wait_for("avatar settings", lambda: node("avatar-color-pearl"))
        wait_for("live model catalog", lambda: node("agent-model-picker"))
        press("avatar-shape-bean")
        wait_for("bean preview", lambda: "checked" in (node("avatar-shape-bean") or {}).get("states", []))
        capture(screenshot)
        press("avatar-color-pearl")
        press("avatar-shape-pebble")
        set_value("agent-name", "Scout")
        if chat()["agent"]["model"] != "gpt-6.1-terra":
            raise RuntimeError("Expected Terra as the default model")
        reveal_setting("agent-workspace")
        set_value("agent-workspace", str(repo))
        press("agent-save")
        wait_for("saved agent", lambda: chat()["agent"]["name"] == "Scout")
        if chat()["agent"]["workspace"] != str(repo):
            raise RuntimeError("The working folder path changed when it was saved")
        wait_for("returned to Bud directory", lambda: node("edit-bud-agt_buddy"))
        press("chat-conv_buddy")
        wait_for("conversation view", lambda: tree_has("Message Scout"))

        set_value("chat-input", "Reply with exactly BUDDY_CHAT_HELLO.")
        composer_keys("Cmd+Down", "Shift+Enter")
        wait_for("newline in unsent draft", lambda: "\n" in node("chat-input").get("value", ""))
        if chat()["messages"]:
            raise RuntimeError("Shift+Enter sent the draft")
        composer_keys("Enter")
        reply = wait_for("plain reply", lambda: reply_matching(lambda m: "BUDDY_CHAT_HELLO" in m["text"]), 90)
        if "BUDDY_CHAT_HELLO" not in reply["text"] or reply.get("did_work", False):
            raise RuntimeError(f"Expected a plain answer without tool activity: {reply}")
        print("Plain chat returned a reply without tool activity.", flush=True)

        # A file and an image carry independent answers; neither is in the prompt.
        wait_for("native attachment button", lambda: node("attach"))
        wait_for("native dictation button", lambda: node("dictate"))

        def png_chunk(kind, payload):
            block = kind + payload
            return struct.pack(">I", len(payload)) + block + struct.pack(">I", zlib.crc32(block))

        image = (b"\x89PNG\r\n\x1a\n" +
                 png_chunk(b"IHDR", struct.pack(">IIBBBBB", 64, 64, 8, 2, 0, 0, 0)) +
                 png_chunk(b"IDAT", zlib.compress((b"\x00" + b"\xff\x00\x00" * 64) * 64)) +
                 png_chunk(b"IEND", b""))
        attach_marker = "ATTACH_" + os.urandom(5).hex()
        upload = api("conversations/conv_buddy/messages", {
            "text": "From the two attachments, tell me the solid image's color and the exact token from notes.txt. "
                    "Answer directly, without delegating.",
            "attachments": [
                {"name": "swatch.png", "data": base64.b64encode(image).decode()},
                {"name": "notes.txt", "data": base64.b64encode(attach_marker.encode()).decode()},
            ],
        })
        wait_for("visible uploaded image", lambda: tree_has("swatch.png"))
        attachment_reply = wait_for("reply reading attached files", lambda: reply_matching(
            lambda m: m["task_id"] == upload["id"]), 90)
        if attach_marker not in attachment_reply["text"] or "red" not in attachment_reply["text"].lower():
            raise RuntimeError(f"Model didn't read both attachments: {attachment_reply}")
        print("The model read the attached image and text file. The upload appeared in native chat.", flush=True)

        send("Run /bin/sh ./show-result.sh in the working folder and tell me the exact value it prints. "
             "The script waits briefly for a file created by my test, so let it finish.")
        work = wait_for("independent work", lambda: next(
            (w for w in chat().get("work", []) if w["status"] == "running" and w.get("can_steer")
             and w.get("activity_kind") == "running"), None
        ), 90)
        active_id = work["id"]
        wait_for("background work in the inspector", lambda: node("stop-work-" + active_id))
        capture(activity_screenshot)
        api("tasks/" + active_id + "/steer", {
            "text": "In the result, also include the literal word BUDDY_STEERED alongside the exact output.",
        })
        wait_for("background correction delivered", lambda: any(
            c["state"] == "applied" for w in chat()["work"] if w["id"] == active_id for c in w.get("steering", [])
        ), 20)
        set_value("chat-input", "Reply with exactly BUDDY_CONCURRENT, without using tools.")
        composer_keys("Enter")
        side_reply = wait_for("chat reply during the tool run", lambda: reply_matching(
            lambda m: m.get("origin") == "chat" and "BUDDY_CONCURRENT" in m["text"]
        ), 35)
        if side_reply.get("did_work", False) or not any(
            w["id"] == active_id and w["status"] == "running" for w in chat()["work"]
        ):
            raise RuntimeError("The companion did not chat independently of the worker")
        close()

        marker = "REPO_RESULT_" + os.urandom(6).hex()
        (repo / "result.txt").write_text(marker + "\n")
        reply = wait_for("background reply with desktop closed", lambda: reply_matching(
            lambda m: m["task_id"] == active_id
        ), 90)
        if marker not in reply["text"] or "BUDDY_STEERED" not in reply["text"] or not reply.get("did_work", False):
            raise RuntimeError(f"Agent didn't report the real repo result: {reply}")
        if app_pid():
            raise RuntimeError("Desktop was running when the background reply arrived")
        print("The companion chatted during a delegated run. Steering and completion worked independently.", flush=True)

        launch()
        wait_for("saved reply in reopened chat", lambda: tree_has(marker))
        capture(reply_screenshot)
        avatar = chat()["agent"]["avatar"]
        if avatar["color"] != "pearl" or avatar["shape"] != "pebble":
            raise RuntimeError(f"Expected saved appearance: {avatar}")
        print("Reopened chat shows the saved reply and edited agent.", flush=True)

        (repo / "alias").symlink_to(state, target_is_directory=True)
        (repo / "check-isolation.py").write_text(
            "import json, os, time\n"
            "results = {}\n"
            "for file, flags in [('data/buddy.db', os.O_WRONLY), "
            "('alias/server.token', os.O_RDONLY), ('dist/buddyd', os.O_WRONLY)]:\n"
            "    try:\n"
            "        os.close(os.open(file, flags))\n"
            "    except PermissionError:\n"
            "        results[file] = 'denied'\n"
            "    else:\n"
            "        results[file] = 'allowed'\n"
            "with open('probe.tmp', 'w') as report:\n"
            "    json.dump({'pid': os.getpid(), 'results': results}, report)\n"
            "os.rename('probe.tmp', 'probe.json')\n"
            "time.sleep(30)\n"
            "with open('tool-finished', 'w') as marker: marker.write('finished')\n"
        )
        send("Run exactly /usr/bin/python3 ./check-isolation.py in the working folder. "
             "This is a Buddy test and I will interrupt the command while it sleeps.")
        probe = wait_for("sandbox command to start",
                         lambda: json.loads((repo / "probe.json").read_text()), 90)
        if len(probe["results"]) != 3 or set(probe["results"].values()) != {"denied"}:
            raise RuntimeError(f"Unexpected sandbox permissions: {probe['results']}")
        active = [w for w in chat()["work"] if w["status"] == "running"]
        if len(active) != 1:
            raise RuntimeError("Expected one running command")
        stop_key = "stop-work-" + active[0]["id"]
        wait_for("Stop in the inspector", lambda: node(stop_key))
        press(stop_key)
        reply = wait_for("cancelled reply", lambda: reply_matching(lambda m: m["task_id"] == active[0]["id"]))
        if reply["status"] != "cancelled":
            raise RuntimeError(f"Expected cancellation: {reply}")
        result = subprocess.run(["ps", "-p", str(probe["pid"]), "-o", "stat="],
                                capture_output=True, text=True, check=False)
        if (result.stdout.strip() and not result.stdout.strip().startswith("Z")) or (repo / "tool-finished").exists():
            raise RuntimeError("The Codex tool continued running after cancellation")
        print("Workspace edits worked, protected files stayed inaccessible, and Stop ended the tool.", flush=True)

        press("New Bud")
        reveal_setting("agent-role")
        set_value("agent-name", "Forge")
        set_value("agent-role", "Coding specialist")
        wait_for("enabled Create Bud", lambda: (n := node("agent-save")) and n.get("label") == "Create Bud"
                 and "disabled" not in n.get("states", []))
        press("agent-save")
        forge = wait_for("created agent", lambda: next(
            (a for a in api("workspace")["agents"] if a["name"] == "Forge"), None
        ))
        forge_path = Path(chat(forge["conversation_id"])["agent"]["workspace"])
        if forge_path == Path(original["workspace"]) or not forge_path.is_dir() or forge["role"] != "Coding specialist":
            raise RuntimeError("Forge's role or workspace did not persist")
        wait_for("Forge chat", lambda: tree_has("Message Forge"))
        press("New group chat")
        wait_for("member choices", lambda: node("member-" + forge["id"]))
        set_value("group-name", "Workshop")
        press("member-agt_buddy")
        press("member-" + forge["id"])
        wait_for("enabled Create group", lambda: (n := node("form-save")) and "disabled" not in n.get("states", []))
        press("form-save")
        group = wait_for("created group chat", lambda: next(
            (g for g in (api("workspace").get("groups") or []) if g["name"] == "Workshop"), None
        ))
        wait_for("group composer", lambda: node("recipient"))
        send("Please hand this off to Forge, our coding specialist. In Forge's own workspace, "
             "create workshop.txt containing exactly BUDDY_TEAMWORK. Have Forge report the result here.")
        done = wait_for("teammate work result", lambda: reply_matching(
            lambda m: m.get("origin") == "handoff", group["id"]
        ), 100)
        if done["agent_id"] != forge["id"] or (forge_path / "workshop.txt").read_text().strip() != "BUDDY_TEAMWORK":
            raise RuntimeError(f"Forge did not finish the delegated work: {done}")
        wait_for("linked result in group", lambda: node("view-job"))
        capture(reply_screenshot)

        # Use a different HTTPS origin and the same pinned Buddy identity.
        owner_dir, worker_dir, device_repo = (directory / name for name in ("owner", "worker", "device-workspace"))
        for folder in (owner_dir, worker_dir, device_repo):
            folder.mkdir(mode=0o700)
        owner_file = owner_dir / "connection.json"
        worker_file = worker_dir / "connection.json"
        worker_bin = directory / "buddyworker"
        subprocess.run(["go", "build", "-o", str(worker_bin), "./cmd/buddyworker"], cwd=ROOT, check=True)
        subprocess.run([dist / "buddyd", "-data-dir", state, "-export-connection", owner_file,
                        "-server-url", f"https://localhost:{port}"], check=True)
        subprocess.run([worker_bin, "-enroll", owner_file, "-connection", worker_file, "-name", "Workshop Mac"], check=True)
        (device_repo / "verify-worker-env.sh").write_text(
            "#!/bin/sh\n"
            'if [ "${BUDDY_SMOKE_CANARY+x}" = x ]; then\n'
            "  printf 'exposed\\n' > device-proof.txt\n"
            "else\n"
            "  printf 'BUDDY_DEVICE_WORK\\n' > device-proof.txt\n"
            "fi\n"
        )
        worker = subprocess.Popen([worker_bin, "-connection", worker_file, "-workspace", device_repo, "-allow-edits"],
                                  env={**os.environ, "BUDDY_SMOKE_CANARY": "synthetic-worker-credential"})
        wait_for("worker online", lambda: any(d["name"] == "Workshop Mac" and d["online"] for d in api("devices")))
        verify_new_address()
        close()
        launch()
        navigate("nav-connections")
        wait_for("hosted connection", lambda: tree_has("Hosted Buddy server"))
        wait_for("connected device shown", lambda: tree_has("Workshop Mac"))
        press("chat-" + group["id"])
        wait_for("group chat reopened", lambda: node("recipient"))
        send("Run exactly /bin/sh ./verify-worker-env.sh in the selected device's working folder. "
             "The script itself writes device-proof.txt. Do not create the file any other way. "
             "Before doing the work, ask me which device to use with a clickable choice card. "
             "Use the exact option labels Buddy server and Workshop Mac. Do not start until I pick.")
        card = wait_for("model choice card", lambda: reply_matching(lambda m: m.get("card"), group["id"]), 90)
        options = card["card"]["options"]
        index = options.index("Workshop Mac")
        button_key = f'choice-{card["task_id"]}-{index}'
        wait_for("native choice button", lambda: node(button_key))
        capture(card_screenshot)
        press(button_key)
        wait_for("card selection saved", lambda: reply_matching(
            lambda m: m["task_id"] == card["task_id"] and m.get("card", {}).get("choice") == index, group["id"]
        ))
        finished = wait_for("device task finished", lambda: next(
            (w for w in chat(group["id"]).get("work", []) if w.get("device") == "Workshop Mac"
             and w["status"] == "succeeded"), None
        ), 100)
        wait_for("device result posted", lambda: reply_matching(lambda m: m["task_id"] == finished["id"], group["id"]))
        if (device_repo / "device-proof.txt").read_text().strip() != "BUDDY_DEVICE_WORK":
            raise RuntimeError("The worker did not write its workspace proof or inherited the environment canary")
        print("Pinned HTTPS, native card, and device execution with an isolated environment worked.", flush=True)

        press("new-routine")
        wait_for("routine form", lambda: node("routine-name"))
        set_value("routine-name", "Workspace check")
        set_value("routine-prompt", "List new files and flag anything that needs my attention.")
        wait_for("first run preview", lambda: tree_has("First run "))
        wait_for("routine draft visible", lambda: tree_has("List new files and flag anything"))
        capture(routine_screenshot)
        press("cadence-Custom")
        wait_for("custom interval input", lambda: node("routine-interval"))
        set_value("routine-interval", "120")
        wait_for("enabled Schedule routine", lambda: (n := node("form-save")) and "disabled" not in n.get("states", []))
        press("form-save")
        wait_for("saved custom routine", lambda: any(
            r["name"] == "Workspace check" and r["minutes"] == 120 for r in chat(group["id"]).get("routines", [])
        ))
        wait_for("returned to group", lambda: node("chat-input"))
        send("Please create a routine named Smoke pulse that runs every minute. Each time, list the files in your workspace "
             "and include the exact word BUDDY_ROUTINE in the result.")
        scheduled = wait_for("routine saved from chat", lambda: next(
            (r for r in chat(group["id"]).get("routines", []) if r["name"] == "Smoke pulse" and r["minutes"] == 1), None
        ), 90)
        wait_for("routine confirmation", lambda: reply_matching(
            lambda m: "Routine scheduled" in m["text"] and "Smoke pulse" in m["text"], group["id"]
        ))
        wait_for("routine in workspace sidebar", lambda: node("routine-menu-" + scheduled["id"]))
        wait_for("routine confirmation in chat", lambda: tree_has("Routine scheduled"))
        capture(reply_screenshot)
        print("Group handoff worked. Both the form and a chat request saved routines.", flush=True)
        close()
        wait_for("scheduled result with desktop closed", lambda: reply_matching(
            lambda m: m.get("origin") == "routine" and "BUDDY_ROUTINE" in m["text"], group["id"]
        ), 130)
        group_ref = next(g for g in api("workspace")["groups"] if g["id"] == group["id"])
        if not group_ref.get("has_new_result") or app_pid():
            raise RuntimeError("The background routine did not leave an unread result with the desktop closed")
        print("The daemon ran a routine with the desktop closed and left an unread result.", flush=True)
    finally:
        try:
            if worker is not None:
                worker.terminate()
                try:
                    worker.wait(timeout=20)
                except subprocess.TimeoutExpired:
                    worker.kill()
                    worker.wait()
        finally:
            try:
                # Release the test command even if an assertion fails before writing its result.
                (repo / "result.txt").touch(exist_ok=True)
                if pid := app_pid():
                    os.kill(pid, signal.SIGTERM)
                    wait_for("fixture app shutdown", lambda: not app_pid(), 8)
            finally:
                subprocess.run([RUN, "--stop-server"], env=env, check=False)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    scope = parser.add_mutually_exclusive_group()
    scope.add_argument("--connections-only", action="store_true")
    scope.add_argument("--buds-only", action="store_true")
    scope.add_argument("--workbench-only", action="store_true")
    parser.add_argument("--screenshot", type=Path)
    parser.add_argument("--activity-screenshot", type=Path)
    parser.add_argument("--reply-screenshot", type=Path)
    parser.add_argument("--routine-screenshot", type=Path)
    parser.add_argument("--card-screenshot", type=Path)
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix="buddy-smoke-", dir="/tmp") as tmp:
        run(Path(tmp).resolve(), args.screenshot, args.activity_screenshot, args.reply_screenshot,
            args.routine_screenshot, args.card_screenshot, args.connections_only, args.buds_only, args.workbench_only)
