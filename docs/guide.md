# Setup and usage

[Back to Buddy](../README.md)

Detailed instructions for running Buddy, connecting devices, and configuring its capabilities.
Run shell commands from the repository root unless a section says otherwise.

- [macOS desktop](#run-on-macos)
- [Using Buddy](#using-buddy)
- [Development builds](#development-builds)
- [Chats, work, and storage](#chats-work-and-storage)
- [First launch](#first-launch-on-macos)
- [Hosting a server](#host-buddy-on-another-machine)
- [Updates and releases](releases.md)
- [iPhone companion](#buddy-for-iphone)
- [Worker devices](#run-work-on-another-device)
- [Moving a server](#move-the-home-server)
- [Integrations, watches, and memory](#proactive-watches-and-unattended-operation)
- [Voice calls](#live-voice-calls)
- [Bud personality](#bud-personality-and-memory)
- [Skills](#skills)
- [MCP tools](#custom-mcp-tools)
- [Architecture and checks](#how-it-works)

## Run on macOS

Download [Buddy.dmg](https://github.com/Hortlund/buddy/releases/latest/download/Buddy.dmg)
from the latest published release, open it, and drag Buddy onto Applications.
Eject the image and open Buddy from Applications. The universal app supports
macOS 12 and later on Intel and Apple Silicon, with a local server included.
Choose **Start on this Mac** or connect to a Buddy home hosted elsewhere.
Desktop installation does not require Go or a source checkout. Install and
authenticate Codex on the machine running the model runtime when using Codex.
Use **Settings → About → Check for updates…** for future desktop releases.

Before the first signed release is published, build from source using the steps
below. The same steps work for development builds afterward.

### Build from source

Install Go 1.27.4+, Git, and a Codex CLI with named permission profiles.
GPT-6.1 Terra requires Codex CLI 0.159.0 or later. [Codex models](https://learn.chatgpt.com/docs/models)

Run `codex login` on the server to use the CLI's sign-in, or save an OpenAI
API key in **Connections → Runtimes → Codex → Manage**. A saved key takes priority for new runs. Removing
it restores the server's existing sign-in or environment key. For headless
launches Buddy can also read `OPENAI_API_KEY` or `CODEX_API_KEY` from the daemon
environment.

Run this command from the repository root.

```sh
./script/build_and_run.sh
```

## Using Buddy

For daily development, register project checkouts in Connections and keep host
capabilities current. Buds can use installed Go, Node, Python, Rust, Java, .NET,
Swift, Ruby and build tools through Codex jobs. Use `/code` for implementation,
`/debug` for reproduction and verification, and `/review` for a read-only review;
these workflows are also discoverable by the model during ordinary chat.

Buddy Home can run project work itself, including on a VPS. A connected Mac or
Linux worker can run it instead when its capabilities or checkout are needed.
Register a logical project in Connections and set its Buddy Home checkout there.
On another worker, add the same project ID and that machine's absolute checkout
path to its private `execution.json` (or the file passed to `buddyworker -config`),
then restart the worker. Project paths are host-specific; Buddy does not copy a
repository or credentials between machines. Keep the worker online and enable its
shared pool if it should accept automatic placement; enable pool edits for writing.
Explicitly named hosts can keep work queued while offline.

Project assignments use `workspace_mode` to choose `auto`, `worktree` or `checkout`.
Automatic edits in a clean registered Git project get an isolated worktree and
branch. For a dirty checkout, automatic mode keeps its pending changes in place instead
of hiding them in a worktree based on HEAD. Explicit checkout mode also works in
the original directory, including its uncommitted changes. Workers preserve
unrelated edits, and conflicting active checkout sessions are rejected, including
overlapping parent and child directories. A resumed project job keeps its previous
checkout or worktree choice even if the original checkout becomes clean or dirty.
Dirty checkouts are never silently reset or stashed.
Read-only jobs use the original checkout. A new repository can be initialized or
cloned inside a worker's assigned writable workspace. Workers inspect repository
instructions, build, run relevant tests and inspect their final diff before
reporting results. Worktrees with unmerged work are retained for inspection.
Use Git or explicit artifacts to transfer work between hosts; a remote checkout
is not automatically synchronized with a local one.

GitHub lookups support paginated open PRs and issues, changed-file lists,
individual patch chunks, source files at a ref, discussion and inline review
comments, workflow runs and their job steps. Reviews should record both head and
base commits, read beyond the preview, and restart if either changes. GitHub may
omit large or binary patches and limits its file listing to 3,000 files; use a
checkout for complete coverage when needed.

Use `/inbox` or ordinary chat for Gmail triage. Search supports continuation
cursors, and conversation reads include five message excerpts per page (up to 4,000
characters each), newest pages first with messages ordered chronologically within each page. The model
can request older pages. Gmail access remains read-only: reply drafts appear in
chat, and are not saved or sent in Gmail.


For Docker-based inline code execution, prepare the runtimes you want to use.
Buddy uses installed images and can delegate work when an image is unavailable.

```sh
docker pull docker.io/library/golang@sha256:4fa7c968ba93f1635670522dbb01a2c78150443e1385746924566079f36570ea
docker pull docker.io/library/python@sha256:3fe1a0b6c2d6b2ebb572906cdabeca75aeb558c0c17db5ffefbcf8094871c32b
docker pull docker.io/library/node@sha256:9d266751ca3d7fdbe651c878ffd5ad0a42fed49a18c283509267c97f025b7496
docker pull docker.io/library/ruby@sha256:d7c312a48736dd291d7ce043c847f04dfde9bdf7334491ca6a545a4a184395be
```

1. Pick a Bud in the left sidebar and send a message. Normal conversation
   gets a direct reply. A Bud can inspect workspace files, look up GitHub
   records, read connected Gmail and monitoring data, open text attachments,
   and check current job progress in the same turn. Buds can discover relevant
   installed skills and look up public information in a separate Codex session
   scoped to the user's request. A Bud can save a small Markdown, text, CSV, or
   JSON file in its editable workspace when the request authorizes the save.
   For longer research and development, it can start a Codex background job.
   Keep chatting while it runs, or quit Buddy and open it later. The result
   lands in the originating chat.
   Ask a Bud to run or verify a short standalone snippet in Go, Python,
   JavaScript, TypeScript, Bash, Ruby or SQL. Only available isolated runtimes
   appear in its tool catalog. The Go image also enables Bash; the Python image
   enables in-memory SQLite. The Node image enables JavaScript and TypeScript.
   TypeScript runs with [Node type stripping](https://nodejs.org/api/typescript.html),
   without a separate type check; ask for a project job to run tsc or dependency-based tests.
   On native Linux, TypeScript requires Node 24 or newer.
   Each execution has its own output and exit status in chat. The server
   uses a network-disabled sandbox. On macOS, inline runs require running
   Docker and a pinned runtime image. On Linux, the installer delegates the
   cgroup v2 memory and process controllers for native runs with bubblewrap.
   For an existing systemd service, add `Delegate=memory pids` under `[Service]`,
   reload systemd, and restart Buddy with the updated server binary. A service
   without native limits can use the same Docker setup as macOS. Execution has
   a 256 MiB memory limit and a 32-process limit. Go compilation uses a separate
   sandbox with a 768 MiB memory limit. For programs that write files, repository
   builds, longer tests, or work on another machine, the Bud can start a
   background job from the request. Name the project or device when the job
   depends on it. On the home server, read-only jobs can use temporary build
   space while their working folder stays read-only. The chat shows the latest
   command and its output during the job.
   If a job needs input, choose **Reply to job** in chat or in Work. Your
   reply resumes the same job.
   Use the paperclip for files or paste an image into the composer. Each message
   can include up to five files totaling 8 MB. PNG, JPEG, GIF and WebP can be
   passed as visual input. Other files are available to background tools.
   Click a sent attachment to save a copy. The microphone button uses macOS
   Dictation to fill the draft. Enable it in System Settings → Keyboard if needed.
   Chat renders Markdown, fenced code, and unified Git diffs in both directions.
   Buds can link web sources, workspace files, folders, and jobs directly in their
   replies. File links open beside the desktop chat or in the iPhone's native
   preview; job links open Work. File links keep their workspace binding and show
   an error if the Bud has switched to another folder. Managed server moves
   preserve these bindings. Job links follow work into a group when a call grows.
   Files on another worker device must be brought into the server workspace before
   they can be previewed this way.
2. Use **+** next to **Buds** to create a Bud with a role, instructions, model,
   and avatar on Mac. You can ask for a new Bud in chat or in a call on either
   device. Ask for a draft to review the profile before creating it.
   **Bud settings** lists every Bud and lets you choose your
   **Primary Bud**, whose chat Buddy opens on launch. Each Bud starts with a
   dedicated writable workspace that you can change from its profile. The
   default model is GPT-6.1 Terra. Use **Remove** in Bud settings to take a
   Bud out of active chats and stop its routines. **Removed Buds** keeps
   its chat history and workspace files accessible.
   In **Edit Bud → Worker intelligence**, keep **Let Bud decide** for automatic
   worker models, reasoning effort and speed. At the start of an assignment, a
   bounded Codex decision uses that machine's live model catalog and the actual
   task, complexity and verification needs. Coding and review can use a stronger
   model and higher effort while straightforward triage can use less. The selected
   settings are validated against the chosen model and recorded before work starts;
   local and remote retries keep the decision while its model and options remain
   supported. Moving to a host with a different model catalog can trigger a new
   decision. Catalog or decision failures fall
   back to the Bud's default settings. Ordinary chat uses the default model without
   an extra decision pass. **Use fixed settings** pins the default model, effort
   and speed instead. Older saved task profiles remain compatible; in automatic
   mode they are suggestions rather than fixed routing rules. These controls need
   Buddy Home protocol 87 and an updated buddyworker. Realtime voice keeps its
   voice model; jobs started from calls use the same worker planning.
   Saving a Bud from an older client preserves its saved automatic or fixed mode.
3. Use **+** next to **Group chats** to bring Buds into the same conversation.
   Buds receive the current team roster, roles, primary Bud, group lead, active
   assignments, dependencies, and recent joins or departures. They can coordinate
   separate assignments and ask a teammate for a contribution. Other chats share
   job titles and status, rather than private transcripts, memories, or results.
   An identical active assignment for the same Bud and workspace is reused;
   different work and requests for another perspective stay separate.
   Pick a **Lead Bud** for default replies and calls, or change it from the chat
   header. New groups default to the Primary Bud when they include it. The
   group routes messages to a suitable member. Address a Bud by name to direct a
   request. Buds see the shared transcript and can hand off work to each other
   using their own workspaces.
   In a direct chat, group chat, or call, you can give several Buds separate
   assignments in one request, such as “Forge, run the tests. Scout,
   review the changes. Forge, compare both findings when they're ready.”
   Independent jobs can run at the same time. You can keep chatting or ask
   for progress, and results appear in the chat that started the work.
4. The **Workspace** sidebar shows the selected Bud, recent top-level files
   and folders, background work, and routines. Click a file or **Browse workspace files**
   to open it in a tab beside the conversation. Buddy keeps the open file and
   unsaved edits when switching chats.
   The viewer previews Markdown, Git patches, PNG, JPEG, GIF,
   and WebP, and edits UTF-8 text and code up to 512 KB. Use **Save** or **⌘S**
   to write the file back to the server. If it changed on disk, Buddy keeps your
   draft and asks you to reload. Local files can still be revealed in Finder.
   A dot next to another chat signals a new background result.
   Open **Work** to see jobs across chats. Select a job for its assignment,
   activity, and result, or choose **Ask about this job** to open a linked draft.
   The Workspace shows three jobs and links to the full list for that chat.
   Use **Load older jobs** in Work to browse past jobs.
5. Use **+** next to **Routines** to give the selected agent a repeating
   assignment, or ask your agent in chat to create one. Choose Hourly, Daily,
   Weekly, or a custom interval from one minute to one week. The first run
   starts after one interval. The daemon
   runs each occurrence and posts the result to that chat. Pause, resume, or
   delete the schedule from its menu.
6. Open **Menu → Settings** to choose Light or Dark and set separate languages
   for chat and live calls. Leave a language on Automatic to let Buddy follow
   the conversation. The sidebar shows the desktop version next to Buddy,
   and **Settings → About** shows the full version, channel, and build number.

## Updating Buddy

Release builds keep the Go/MyGo desktop and the server independently updatable.
On macOS, open **Settings → About → Check for updates…**. MyGo checks, verifies,
installs, and relaunches the desktop. It offers automatic update checking using
its own preferences. Install the app in Applications before updating it.

For an installer-managed server, run `buddy-server update` in a terminal on that
server host, including when you connect to it remotely from your desktop. Use
`buddy-server update v0.2.0` to choose a specific newer stable release. The updater
preserves the server address, TLS identity, credentials, phone pairings, and
workspaces. It refuses an upgrade while jobs are running or queued; finish them
and retry. The desktop reconnects after the brief restart.

**Settings → About** shows both versions and lets you copy the hosted update
command. A local desktop-managed server uses **Update local server** after the
desktop itself is updated. Update worker binaries on their own machines from the
matching server archive, after their assigned work finishes, and restart them
with their existing connection file and arguments. Update the server first,
then clients and workers; features on older servers keep their existing protocol
checks.

Existing hosted installations need one installer rerun to gain the update command.
That refreshes management tools while preserving the running daemon. Source
builds keep using `./script/build_and_run.sh`; they do not enable desktop updates.
See [releases and recovery](releases.md) for publishing and failed upgrades.

## Development builds

Local builds default to `0.1.0-dev` with a UTC build timestamp as SemVer metadata.
Set `BUDDY_VERSION` and `BUDDY_BUILD_NUMBER` to build a specific release.

```sh
BUDDY_VERSION=0.1.0-beta.1 BUDDY_BUILD_NUMBER=3 ./script/build_and_run.sh
BUDDY_VERSION=0.1.0-nightly.20261106 BUDDY_BUILD_NUMBER=4 ./script/build_and_run.sh
```

### Google sign-in in distributed builds

To include Google sign-in in a macOS beta, enable Gmail API in Buddy's Google
Cloud project and create a **Desktop app** OAuth client. Pass the downloaded
client JSON to the packager. Beta testers can then use **Continue with Google**
from Connections on a fresh Buddy server.

```sh
BUDDY_VERSION=0.1.0-beta.1 BUDDY_BUILD_NUMBER=3 \
  BUDDY_GMAIL_DESKTOP_CLIENT=/path/to/buddy-google-desktop-client.json \
  ./script/build_and_run.sh --verify
```

For an invite-only beta, add testers under **Google Auth Platform → Audience**.
External projects in Testing allow up to 100 test users, and Gmail grants
expire after seven days. Broader release requires Google's restricted-scope
verification for Gmail read access. [OAuth app states](https://developers.google.com/identity/protocols/oauth2/production-readiness/overview) [Restricted-scope verification](https://developers.google.com/identity/protocols/oauth2/production-readiness/restricted-scope-verification)

## Chats, work, and storage

Press Enter to send and Shift+Enter to insert a new line. During a chat
turn, **Stop** takes the place of **Send** and a follow-up can be queued or
steered. For delegated work, open the job in **Work** to inspect progress,
ask about it, or stop it. Live steering uses the Codex App Server with
a file-based sign-in or an API key. [Codex App Server](https://learn.chatgpt.com/docs/app-server)

Buds interpret requests and choose whether to reply, use a tool, or hand off
larger work. Buddy checks the action’s scope and requires confirmation for
deletions. An explicit address in a group chat takes precedence over automatic
routing. To use the [OpenAI Decisions API](https://developers.openai.com/api/docs/guides/decisions)
for automatic group routing, set `BUDDY_DECISIONS_ROUTING=1` in the server
environment before starting it. This uses the server’s OpenAI API key and a
short excerpt from the current group chat. The Codex router remains available
when Decisions has no confident result.

Ask a Bud to generate an image in chat. The server’s OpenAI API key needs
access to [image generation](https://developers.openai.com/api/docs/guides/image-generation).
Buddy generates one PNG from a text description and keeps it in the chat.
Use **Save image** on desktop or **Share image** on iPhone.

Buddy stores managed workspaces under `~/Library/Application Support/buddy-workspaces`
on macOS, separately from its database and binaries. Override the root with
`BUDDY_WORKSPACES_DIR` for the launch script or `-workspaces-dir` for the
daemon. Existing managed workspaces move to the new name. Custom working folders and edit permissions are preserved.
Buddy's runtime files and credentials stay outside tool access. To ask an
agent to edit Buddy itself, use a separate checkout.

The local setup starts a background service for the current macOS login
session on `https://127.0.0.1:7331`. The client verifies the daemon's saved
certificate before authenticating. It reuses the service when you reopen the app.
When an older server is incompatible with the desktop, Run upgrades the server
once its queue is empty. If work is still pending, Run asks you to try again
after it finishes.
Data and logs are stored in `.buddy/`. Set `BUDDY_PORT` or `BUDDY_DATA_DIR`
to use a different port or data directory. To stop or rebuild the service,
run `./script/build_and_run.sh --stop-server`. Restarting the server preserves
queued requests and reports interrupted runs in the chat. Read-only watch
investigations can retry after restart using their saved evidence.

## Custom MCP tools

In **Connections → MCP servers → Add MCP server**, choose a name and a transport.
For Streamable HTTP, paste the server's HTTPS URL and an optional bearer token.
For example, use `openai-docs` and `https://developers.openai.com/mcp` to add
OpenAI's public documentation tools. HTTP is available for loopback URLs on the
Buddy server.

For STDIO, enter the executable installed on your Buddy server and one argument
per line. The command runs with that server account's access, so install trusted
commands outside Bud workspaces and use absolute paths to existing scripts.
Buddy verifies the executable and explicit file arguments before launching.
Review and save local commands again after upgrading Buddy from an older MCP
setup or updating those files. Under **Tool access and environment**, you can
forward named variables from the Buddy daemon's environment. Buddy reserves
`OPENAI_API_KEY` and `CODEX_API_KEY` for Codex.

Choose **Save and enable**, then **Test** to check the handshake and see which
tools are callable. Buds can use tools that declare themselves read-only by
default. To permit other tools on a trusted server, enter their exact names in
**Allowed tools** and select **Preapprove listed tools**. This lets Buds call
the listed tools without a separate confirmation, including tools that can
change external data. The menu beside each server lets you edit, disable,
re-enable, or remove it. Buddy stores the settings with its server state and keeps bearer tokens out
of desktop responses. Enabled tools apply to new Codex chats and jobs on the
Buddy home server with a file-based Codex sign-in or API key. HTTP authentication in Buddy uses bearer tokens. Voice calls and remote workers
use separate runtimes.

## First launch on macOS

Open the bundled Buddy app or run `./script/build_and_run.sh` with a fresh home.
Choose **Start on this Mac** to create a local home and start its background
service. Buddy keeps running after you close the app while this Mac is awake
and signed in. If the service is offline when you reopen Buddy, start it again
from **Connections → Home & devices**. For unattended hosting across desktop
restarts and sleep, use a separate always-on server.

Choose **Connect to an existing server…** to use an existing home. Buddy verifies its
private owner access file before asking you to reopen the app. After either
path, sign in to Codex on the server or save its API key. You can finish the
Codex setup later from **Connections → Runtimes**.

## Host Buddy on another machine

Buddy uses one active home server for the conversation database, schedules, and
queues. The server can run on your Mac or a Linux machine that stays on. Desktop
clients can share that home, and additional machines can run assigned jobs as workers.

### One-command setup

On the host, paste:

```sh
curl -fsSL https://raw.githubusercontent.com/Hortlund/buddy/main/script/install-server.sh | bash
```

Use a 64-bit Linux host with systemd (Debian/Ubuntu or Fedora), or a Mac with
Apple command line tools and Python 3. Intel/AMD and ARM64 are supported. On
Windows, run
inside WSL2 with systemd enabled. The initial install needs internet access,
`curl`, and admin access on Linux; it installs missing Linux build prerequisites.
You can inspect [the installer](../script/install-server.sh) before running it.
The default installs the latest stable prebuilt release. Use `--release vX.Y.Z`
to select a stable release, or `--ref main` (or a commit) for a source build.
Until the first stable release is published, use `--ref main`.

Setup checks the service manager, required commands and CA certificates, available
disk space, writable install directories, and HTTPS access to its downloads before
creating the Buddy service. Allow at least 2 GiB of free space when downloading
and building runtimes, or 512 MiB when supplying both binaries. On Linux it installs
missing prerequisites through apt or dnf. On Mac it gives instructions for missing command line tools or Python.
Download failures have bounded timeouts and report DNS, proxy, or certificate checks
to make before retrying. Model sign-in remains a separate step; the OpenAI network
probe checks HTTPS connectivity without sending credentials or a model request.

The installer tries loopback port 7331, then selects a free port from the next 63
ports if needed. It saves the choice in the service, management command, desktop
access file, and phone pairing link. It rechecks the port after building. You can
require a particular port when installing a new home:

```sh
curl -fsSL https://raw.githubusercontent.com/Hortlund/buddy/main/script/install-server.sh | bash -s -- --port 7441
```

A busy explicit port stops setup with a clear error. Existing managed homes keep
their port on reruns. Setup does not stop another app to claim a port or modify
firewall rules. Tailscale provides the private route; its own access policies and
your sign-in still apply. An installed but stopped standard Linux Tailscale service
is started when private networking is configured.

The installer downloads checksum-verified Buddy and Codex binaries. An explicit
source build also installs the pinned Go toolchain and builds Buddy. Install
project toolchains separately when agents need them on a prebuilt server.
Setup creates a dedicated `buddy` service account on Linux and enables
`buddyd.service` at boot. On Mac it creates a separate LaunchAgent, which runs
while you stay signed in and awake. An existing home can keep running on
its own port. This creates a **new home**; it does not migrate your desktop's
chats automatically.

Tailscale setup opens a sign-in URL. Use the same account on the host, desktops,
and phones. Buddy stays on loopback; a persistent private TCP forwarding rule
exposes only Buddy's port on your tailnet. Buddy keeps its pinned TLS and device
credentials. Setup preserves unrelated Tailscale routes and refuses to replace a
conflicting route. Tailscale access rules still determine which devices can reach
it. No public port, DNS setup, or Funnel is needed.

On macOS, install and open [Tailscale](https://tailscale.com/download/mac) first.
Approve its VPN permission and sign in, then run `buddy-server tailscale` if needed.
On Linux, the installer can install Tailscale for you. Use a durable server device
in your Tailscale admin settings so key expiry does not unexpectedly interrupt
24/7 access. Check your account's device and access policies.

### Connect your apps

1. Run `buddy-server login` on the host and follow the Codex device sign-in flow.
   Alternatively, connect the desktop and save an OpenAI API key in **Connections**.
   Device sign-in may need enabling in your ChatGPT account or workspace settings.
2. The installer prints the private desktop access file and an `scp` command.
   Run that copy command on your desktop, then select the copied file in
   **Connections → Connect to server… → Reopen Buddy**. Connect both devices
   to Tailscale first. `buddy-server connect` refreshes the file and instructions.
3. Run `buddy-server pair` to display a one-time iPhone QR code. Scan it in Buddy
   for iPhone, or paste the pairing link. Each phone gets its own revocable access.

The desktop access file is a private sign-in containing the HTTPS address,
pinned certificate, and owner token. Transfer it privately. It contains no chat
history or workspace files. Live voice uses its own OpenAI key in **Connections →
Live voice**, even when Codex is signed in.

### Manage the host

```sh
buddy-server status       # Server health and model sign-in
buddy-server logs         # Follow server logs
buddy-server stop         # Stop the service
buddy-server start        # Start it again
buddy-server tailscale    # Finish or repair private networking
buddy-server connect      # Refresh desktop access instructions
buddy-server pair         # One-time phone invitation
```

On Linux the command is `/usr/local/bin/buddy-server`. On Mac it is
`~/.local/bin/buddy-server`; use that full path if your shell cannot find it.
Linux data lives in `/var/lib/buddy`, binaries in `/opt/buddy`. Mac data lives in
`~/.local/share/buddy-server`. State and credentials are private to the service
account. Use `sudo journalctl -u buddyd.service` for Linux startup diagnostics.

Rerunning the installer detects an existing managed install and preserves its
binaries, credentials, data, and running jobs. It also repairs toolchain permissions
from older Linux installers. It does **not** upgrade it. To move
an existing home, use the [move flow](#move-the-home-server), which prevents two
hosts from running the same home. Back up before manually replacing binaries;
stop the service first and retain the existing data directory.

To skip Tailscale (for an existing private network), download the script, then run:

```sh
bash install-server.sh --no-tailscale
```

This leaves Buddy accessible only on the host's loopback address. A separately
configured private TCP tunnel is needed for other devices. You can also build
`buddyd` yourself and adapt [the sample unit](../deploy/buddyd.service).
For a local checkout use `bash script/install-server.sh --source-dir "$PWD"`.
Advanced flags `--binary PATH` and `--codex PATH` use executables you supply.

To remove only Buddy's Tailscale forwarding, check `tailscale serve status`, then
run `sudo tailscale serve --tcp=PORT off` on Linux using the port shown during setup,
or the equivalent command without sudo on Mac. This does not delete your Buddy home or unpair devices.

## Buddy for iPhone

Open [Buddy.xcodeproj](../ios/Buddy.xcodeproj) in Xcode. The **Buddy** scheme
builds the separate iPhone app for iOS 17 and later. Select your development
team or Xcode Personal Team to install it on a physical iPhone. The Go server
is shared with the MyGo desktop app.

In **Settings → Appearance**, choose **Dark**, **Light**, or **System**.
Chats, native question cards, connection cards, Work, and settings share the
desktop palette. Tap the phone in a conversation to call a Bud or invite several
Buds into the same room. See [Live voice calls](#live-voice-calls) for setup.

For a Buddy server on your Mac, install Tailscale on the Mac and iPhone and
connect both to the same tailnet. In **Connections → Home & devices → Pair iPhone**,
choose **Tailscale**, then **Enable private forwarding** if setup is needed.
Buddy checks whether the Mac allows incoming connections and creates a Serve
rule only if the port is free and the configuration still matches what it
checked. If another service uses the port, you can enter a reachable address
manually.

Buddy uses raw TCP so it keeps its own HTTPS certificate and device
credentials. Tailscale Serve shares the route with devices allowed by your
tailnet. To stop sharing, check `tailscale serve status` for the Buddy route,
then run `tailscale serve --tcp=7331 off` with the corresponding port. The Mac
must stay awake for the iPhone to reach a Buddy server running there.
[Tailscale Serve](https://tailscale.com/docs/reference/tailscale-cli/serve)

For an always-on Buddy host, connect that host and the iPhone to the same
tailnet. You can use the same raw TCP forwarding on the host while Buddy
listens on loopback. Connect the Mac desktop to that home using its
[Tailscale address and owner access file](#host-buddy-on-another-machine).
**Pair iPhone** pre-fills the address of a hosted home. Use **Enter address**
when the phone reaches Buddy through another private network.

Generate a QR code and scan it in Buddy on the iPhone. You can also copy the
pairing link and paste it into the app. The invitation expires after five
minutes and works once. The iPhone verifies the server identity and saves
its own access in Keychain.

Chats, watch approvals, and Work are shared with the desktop. Use **Remove**
under **iPhone** in **Connections → Home & devices** to revoke a paired iPhone. After changing your
Buddy home's address or server certificate, disconnect and pair again. Pair
each iPhone again after restoring a backup. Backups preserve chats and jobs
and exclude phone credentials, pairing invitations, and pending push alerts.

For optional push alerts, build the **Buddy Push** scheme with an Apple
Developer Program team. Enable Push Notifications for the app identifier
that matches your build's bundle ID and create an APNs token signing key.
The default bundle ID is `dev.buddy.ios`. If you sign with a different ID,
set that ID in the Xcode build and in `iphone_push.bundle_id` on the server.
Install the key as `apns.p8` with mode `0600` in the server's private
state directory. Add the following to the existing `automations.json` object
and restart the server.

```json
{
  "iphone_push": {
    "team_id": "YOURTEAMID",
    "key_id": "YOUR_KEYID",
    "bundle_id": "dev.buddy.ios",
    "sandbox": true
  }
}
```

Use `sandbox: true` for development-signed builds and `false` for distribution
builds. In the iPhone app, enable notifications in **Settings**. Alerts open
the specific inbox update or job. Requests for input and opted-in watch
updates are included. Finished and failed jobs are a separate toggle.
Push delivery honors the server's configured quiet hours. Keep a private copy
of the APNs key and configuration when moving the server.

## Run work on another device

In **Connections → Worker devices → Add device**, enter a name and the
Buddy server address the worker can reach, or choose the same Mac or SSH tunnel
option. If the worker already uses a tunnel, enter its local forwarded address. Pick a workspace and the job permissions for the worker. Save the setup
file, transfer it privately to the worker, then use **Copy worker command** in
the directory containing the file. Buddy shows when the worker checks in. The setup file can
only claim jobs assigned to that device. The built-in local server listens on
loopback, so pairing a separate machine requires a reachable private listener,
a hosted Buddy server, or an SSH tunnel. If the worker already has SSH access
to the Mac, run `ssh -N -L 7331:127.0.0.1:7331 user@buddy-mac` on the worker
and use `https://127.0.0.1:7331` in its setup file.

Build `buddyworker` on the worker with `go build -o buddyworker ./cmd/buddyworker`
and install the Codex CLI there. You can also enroll from a trusted admin shell.

```sh
umask 077
mkdir -p "$HOME/buddy-worker"
./buddyworker -enroll /path/to/owner.json \
  -connection "$HOME/buddy-worker/studio.json" -name "Studio"
```

Transfer only `studio.json` to the worker. The credential must be owner-only
(0600) inside a private directory separate from the workspace.

```sh
./buddyworker -connection "$HOME/buddy-worker/studio.json" \
  -workspace "$HOME/projects/example"
```

Add `-allow-edits` to permit writes there when the agent also has edits enabled.
Add `-accept-pool` on each worker that can take portable shared jobs. Then ask
Buddy to use any worker for a job that can run in any of their configured
workspaces. The first eligible free worker claims it. Each worker uses its own
files. Jobs on a named machine and paused jobs stay tied to that machine.
Worker tools use a workspace-scoped Codex permissions profile with a minimal
runtime read set. [Codex permissions](https://learn.chatgpt.com/docs/permissions)

Ask the agent to run work on **Studio**. The agent uses the home server by
default, can select a suitable connected device, or can ask with a native choice
card. An offline device can pick up queued work when it returns. An interrupted
assignment stays interrupted to avoid repeating side effects. Upgrade
`buddyworker` with the server to run jobs that include attachments. Use
**Connections → Worker devices → Remove** to revoke a device. Removal interrupts
its assigned jobs and pauses its routines. Existing files remain on the worker.

## Move the home server

Open **Connections → Move this Buddy home**. A Buddy home runs on one server
at a time and can have multiple desktops and workers.

1. Install a current `buddyd` on the new host and let running jobs finish. Queued
   jobs can move with the home.
   For a local server, use **Stop and save move backup…**. For a hosted
   server, stop it with its service manager and use **Copy export command**.
   Buddy saves the archive under `~/.buddy-migration`, which is protected from
   Buds and workers. It contains credentials, chat, attachments, Bud profiles,
   and managed workspace files.
2. Create the private `~/.buddy-migration` folder on the destination with
   **Copy folder setup**, then transfer the archive into it. Enter the destination's
   HTTPS address and the daemon’s listen address separately, then run the copied
   restore and start commands there. Use the default loopback listener behind a
   TLS passthrough proxy, or enter the host’s private IP and port for direct access. The
   restore creates `$HOME/buddy-home`, with server state and managed workspaces
   in separate subfolders. Set up Codex on the new host and transfer any external
   project folders separately.
3. Select **Verify restored server**, then **Reopen Buddy**. Reconnect other
   desktops with a fresh owner access file. Restart each worker with
   `-server https://new-host:7331` and its existing setup file.
4. Confirm or replace each custom working folder in **Bud settings**.
   Buddy holds its queued jobs and routines until you confirm. Resubmit
   standalone repo checks and jobs for previously configured custom paths
   with a folder on the destination.

The source remains retired after export. To roll back to its saved state,
stop the destination first, then run
`buddyd -data-dir /path/to/old-state -rollback-move` on the source. Changes
made on the destination after the move require a new migration to carry
them back.

To exercise a move without touching your own Buddy home, run
`python3 script/smoke_hosting.py` with Docker running and `alpine:3.23` available
locally. It moves a disposable home into a Linux container and reconnects a
worker in a second container.

## Proactive watches and unattended operation

Open **Connections → Integrations** and add a named account or instance. Add
more connections under the same provider for different accounts, tokens, or
monitoring instances. Use **Add watch** next to a connection to pick what
to follow and choose a chat to receive it.

- **GitHub** can reuse the GitHub CLI sign-in on the Buddy server. If needed,
  run `gh auth login` on that server or use **Create a GitHub token** to open
  GitHub with the permissions prefilled. Select the repositories you want
  Buddy to access. **Allow reviewed GitHub writes** enables collaboration
  for the connection, subject to the credential’s GitHub permissions. [GitHub token setup](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens)
- **Gmail** uses **Continue with Google** to select a mailbox and grant read
  access. **Set up Gmail** guides you through the one-time Google Cloud setup
  and imports your **Desktop app** OAuth client JSON. Additional mailboxes
  reuse that client. Servers with Gmail OAuth credentials in `automations.json`
  already have the sign-in setup. Google OAuth projects with an external
  audience in Testing expire Gmail authorization after seven days. [Google OAuth setup](https://developers.google.com/workspace/guides/create-credentials#desktop-app) [Google app audience settings](https://support.google.com/cloud/answer/15549945?hl=en)
- **Grafana** uses the instance URL and a service account token with alert
  read access. **Prometheus** uses the instance URL and an optional bearer
  token. Buddy polls for firing alerts. Use URLs reachable from the Buddy
  server, including any reverse proxy path prefix. Token connections use
  HTTPS or a local loopback IP. [Grafana service accounts](https://grafana.com/docs/grafana/latest/administration/service-accounts/) [Prometheus alerts API](https://prometheus.io/docs/prometheus/latest/querying/api/#alerts)

The **⋯** menu can test, reconnect, or disconnect a connection. Disconnecting
removes the stored credential from the current Buddy database and pauses that
connection’s watches. Existing results stay in Autonomy. Reconnect the same
account or instance to resume its watches. Buddy keeps connection credentials
in its private server database, so server backups and moves carry them too.

In chat, ask “Show open PRs,” “Check Actions for my branch,” or paste a
GitHub pull request or issue link. Buddy uses the GitHub origin of the
Bud’s working folder when no repo is specified. If several accounts are
connected, name the connection you want to use. GitHub cards include
links and, for PRs, a changed-file and diff preview. Grant **Pull
requests: Read** and **Issues: Read** on the selected repositories for
these lookups. You can also ask Buddy to use a PR or issue as context
for work in a local checkout.

On a collaboration-enabled connection, a Bud can open issues, comment on
issues and pull requests, post comment reviews, and open draft pull requests
from branches already on GitHub. Each write shows its destination and full
content in chat for approval. Fine-grained tokens need **Issues: Write** or
**Pull requests: Write**, depending on the action.

In the same chat, ask Buddy to search Gmail or open a matching message, find
Grafana dashboards or firing alerts, or check alerts and run instant PromQL
queries against a Prometheus instance. A Bud can follow up on a result before
replying, such as opening a message found by a search. The latest successful
lookup appears as a card with source links, including alongside a watch draft
or routine change in the same reply. Background jobs and scheduled
routines running on the home server can use these same read tools.

Ask Buddy directly to create a watch. For example, “Tell me here when CI on
`main` fails” or “Watch my Work inbox for email from `billing@example.com`.”
Buddy uses the named connection or asks which one you mean. It resolves the
repo, mailbox label, or instance and shows the draft in chat. Select
**Activate watch** to start it. Multiple watches can share a source if they have
different filters.

You can also add a watch in **Autonomy → Watches → New watch**. The
conversation's lead Bud owns each finding when it arrives. Choose **Digest**
to collect matches in the inbox, **Notify in chat** to post matching events
without a job, **Investigate** for read-only analysis, or **Ask first** to
approve each investigation in the inbox. All positive filters must match.
Chat notifications group overflow in a summary card after ten individual
notifications for a watch in 24 hours.
You can add instructions, ignore matching text, cap each attempt's runtime, and
pause a watch. Pausing clears pending findings that haven't become jobs. Jobs
already queued or running can finish. After ten open approvals or scheduled
investigations within 24 hours, additional matches go to the digest. Archive a
paused watch to free its slot while keeping its history and deduplication state.
Buddy queues up to ten watch jobs at a time and starts queued user work first.

Existing server-configured GitHub and Gmail watches continue to use
`automations.json`. Use the same file for signed GitHub webhooks, notification
delivery, quiet hours, and snapshots. Give it mode `0600` and restart
`buddyd` after changing it. Omit unused settings.

```json
{
  "github_webhook_secret": "replace-with-a-shared-secret",
  "github_token": "replace-with-an-actions-read-token",
  "github_logs": false,
  "gmail_client_id": "replace-with-your-oauth-client-id",
  "gmail_client_secret": "replace-with-your-oauth-client-secret",
  "gmail_refresh_token": "replace-with-your-gmail-readonly-refresh-token",
  "notify_url": "https://notifications.example.net/buddy",
  "notify_bearer": "replace-with-your-relay-token",
  "quiet_timezone": "Europe/Stockholm",
  "quiet_start_hour": 22,
  "quiet_end_hour": 8,
  "snapshot_dir": "/var/backups/buddy",
  "snapshot_keep": 7
}
```

- **GitHub.** Use an Actions read token to poll for failed runs. For immediate
  delivery, configure a signed `workflow_run` webhook where your GitHub setup
  supports it. Forward `POST /hooks/github` from an HTTPS reverse proxy and keep
  Buddy's owner API private. Webhook-only watches require manual redelivery for
  events missed while Buddy was offline. Catch-up polling resumes from saved
  time windows and checks prior attempts of rerun workflows. It looks at runs
  created up to seven days before the cursor. The webhook also covers older reruns.
  Buddy deduplicates by repository, run ID, and attempt. Enable `github_logs`
  if the investigation should include a bounded log excerpt, which may contain
  repository data sent to the configured model. [Workflow runs API](https://docs.github.com/en/rest/actions/workflow-runs)
- **Gmail.** Use a refresh token with `gmail.readonly` scope. Select the label
  by its API ID, such as `INBOX` or `Label_12`. Buddy catches up from its saved
  history cursor or resyncs the current label membership, resuming between
  batches. An expired cursor may surface older messages with that label.
  Investigations use message metadata
  and snippets. Ask for a reply draft in the watch instructions and review the
  draft in Buddy. [Gmail synchronization](https://developers.google.com/workspace/gmail/api/guides/sync)
- **Notifications.** A configured HTTPS relay receives the watch title, state,
  and identifiers for approvals, requests for input, and finished or failed
  investigations. Honor the `Idempotency-Key` header to deduplicate retried
  deliveries. Quiet hours defer relay delivery. If retries are exhausted,
  **Autonomy → Health → Retry now** requeues the unread deliveries. The inbox
  stays available whenever you reopen Buddy.
- **Backups and health.** With `snapshot_dir` configured, Buddy takes a daily
  SQLite snapshot into a separate private directory. Create it with mode `0700`
  and make the Buddy service account its owner. Buddy keeps seven daily
  snapshots by default. Set `snapshot_keep` to a value between 1 and 365 to
  change the limit. A missing or inaccessible destination appears in Health.
  Use the full backup flow
  above for managed workspaces and profiles, and keep a private copy of the
  automation settings separately. **Autonomy → Health** shows queue, worker,
  routine, and source status. For off-host uptime checks, have an independent
  monitor request `/readyz` using the server's TLS certificate.

An interrupted watch job retries on the home server with the same job ID and
saved evidence, up to three attempts. Open **Autonomy → Inbox** to review the
result, follow it into **Work**, or open the original source. **Autonomy →
Memory** holds editable notes that the selected Bud can use in chat. Notes can
include a source and expiration date.

## Live voice calls

In **Connections → Runtimes → Live voice**, save an OpenAI API key with
Realtime access. On a headless server you can set `BUDDY_VOICE_API_KEY`
instead. Voice uses [OpenAI Realtime](https://developers.openai.com/api/docs/guides/realtime-conversations)
and its own key. Reconfigure the voice key
if you move to a new server. Microphone audio travels through your Buddy
server to OpenAI during the call.

Click the phone in a desktop conversation or tap it on iPhone, then allow
microphone access. Desktop rings while connecting. Your Bud greets you in the
call room, your tile shows the
microphone level and the speaking agent lights up. Use **Invite Buds** to
add others. The **Lead Bud** answers questions left open to the room. Say a
Bud’s name, such as “Forge, what do you think?”, or click their tile to give
them the next turn. Buds can invite teammates and pass the floor using the
shared call captions. Invited Buds greet the room. Adding a Bud to a
one-on-one call moves that call’s transcript to a new group chat. Agents
joining a group call are added to that group. **Back to chat** keeps the
call going and opens its chat.

Speak over an agent to interrupt, or use **Mute** and **Hang up**. The
agents can check the chat’s work and routines, read text files in their own
workspace, and queue background work or schedule routines on request. Open
**Work** in the call room to see job progress while you talk, or ask a Bud
for an update. Answer a job’s question right after the call reads it, or say
“For Release review, use the beta branch” to name the job you’re answering.
Results appear in the call’s chat. Ask the team to keep
working after you hang up, or click **Continue after call** to queue a
read-only discussion. After hang-up, each agent posts an update in the
same group chat and the lead agent wraps up the round.

On iPhone, **Chat** collapses the call to a **Return to call** banner. Use
**Invite → Continue collaborating after the call** for a follow-up discussion.
An active call can continue while the iPhone is locked. A competing phone call
or audio interruption ends the Buddy call and leaves completed captions in chat.
Each Buddy home currently supports one active call at a time. Pairing gives the
phone access to calls without copying the OpenAI key onto it. Revoking the
pairing closes an active phone call too.

If a call drops or the mic stays quiet, try **Connections → Live voice → Test
microphone**. On a local install, `.buddy/server.log` and `.buddy/desktop.log`
record call IDs, connection errors and mic startup or retries. The diagnostic
logs omit audio, captions, and credentials. On a hosted install, check the
Buddy server’s service logs for the same call ID.

## Bud personality and memory

**Bud settings → Select a Bud → Instructions** is the in-app editor. Each Bud also gets
`profiles/<agent-id>/AGENTS.md` under the server state directory. Use that file
for versionable Markdown about tone, preferences, and working style. Buddy
reloads it at the start of each chat or job. Project-level instructions still
belong in the project's own workspace.

## Skills

Type `/` at the start of a chat message to see available skills. Click a match
or press Tab to complete it, then add any context and send. Buddy ships with
`/brief` for summaries, `/plan` for planning, `/review` for change reviews,
`/debug` for troubleshooting, and `/create-bud` for drafting a new Bud.
When a Bud is working, send a skill as a queued follow-up. `/review` keeps
one-time handoffs and routines scheduled from the skill read-only.

Try `/create-bud Finch, a CI specialist who investigates failing builds and
reports the likely cause`. You can also ask in plain language. The draft card
shows the proposed name, role, and instructions. Click **Create Bud** to give it
a chat and managed workspace, then **Open chat** to talk to it.

Choose **Skills → Import SKILL.md** to install or update a Markdown skill. The
file uses the [Agent Skills format](https://agentskills.io/specification).
For example, save this as `SKILL.md` and share it with anyone who wants the same
slash command.

```markdown
---
name: release-brief
description: Turn a change list into a short release brief.
---

Group changes by user impact. Summarize migration steps and call out any
known issues. Ask for the change list if one was not provided.
```

Importing it adds `/release-brief` on your Buddy server. Reimport the same name
to update it, or remove it from **Skills**. Work already queued and routines
scheduled from a skill keep the version selected at the time. Upgrade paired
devices to this build of `buddyworker` to run jobs that use skills. Buddy imports
the Markdown instructions from user-invocable skills. In a Codex chat, a Bud
can discover those instructions by name and use them for a matching request.
Explicit slash commands still select and save the workflow for that task and
any work or routines it hands off. A skill uses the selected Bud's normal
permissions. Imported instructions are Markdown.

Built-in command names are reserved for new imports. If an upgrade introduces a
built-in with a name you already installed, your custom definition stays active
and can still be updated. Removing that custom definition restores the built-in.

## How it works

- `ios/Buddy` is the SwiftUI iPhone client for the same HTTPS API.
- `cmd/buddy` is the native Go/MyGo client. `cmd/buddyd` is a separate
  Go process that can also run headless with
  `go run ./cmd/buddyd -addr 127.0.0.1:7331 -data-dir /path/to/buddy-data`.
- The HTTPS API saves messages, queued work, schedules, and results in SQLite.
  The companion chooses whether a message needs a handoff. Buddy commits the
  work item and acknowledgement together, then a separate worker runs it and
  posts the result to the same conversation.
- The server runs one chat turn and one local background job concurrently.
  Each connected worker can run one more job at the same time. A handoff runs
  in a separate Codex session using the assigned Bud's role and permissions.
  Each run has a 45-minute limit.
- Routines are interval-based. When the daemon was offline, it coalesces missed
  intervals into one overdue run. Occurrences of the same routine never overlap.
  A routine with no recoverable original request shows a web search notice.
  Recreate it with the public topic or sources in the request to restore search.

For automated checks, run `go test -race ./...`. With Codex CLI signed in,
`python3 script/smoke_desktop.py` exercises real chat and background work
through a separate native desktop instance and a temporary repository.
Use `--buds-only` for the native settings flow or `--developer-only` to verify
automatic worker settings, project placement, isolated worktree edits and a real
Go test run in a temporary project.
