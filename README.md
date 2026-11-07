# Buddy

Buddy is a personal home for AI companions, called **Buds**. Chat through an idea,
ask for help with everyday tasks, or hand off a project and come back to the result.
Each Bud has its own role, personality, memory, and workspace.

A Go server keeps conversations and work running independently of the apps. Use
the native macOS desktop or the iPhone companion to connect to the same Buddy home.
Host that home on an always-on machine to keep jobs, routines, and watches running
around the clock.

**Buddy is in early development.** The interface, APIs, and data formats are still
evolving. The current way to try it is to build from source. The desktop targets
macOS, the server supports macOS and Linux, and the iPhone app is built with SwiftUI.

## What it does

- **Persistent companions.** Give Buds different roles, customize their appearance,
  and let them build useful memories over time.
- **Conversation and collaboration.** Talk one to one, bring Buds together in group
  chats, or invite them into a live voice call on desktop.
- **Work that keeps going.** Hand off development or research, keep chatting, and
  get results or questions back in the conversation. Review progress in Work.
- **A workspace for each Bud.** Open files beside the chat, preview Markdown and
  code, inspect diffs, and edit text in the desktop app.
- **Routines and watches.** Schedule recurring tasks or ask Buds to watch specific
  activity in connected services.
- **Connections and tools.** Connect GitHub, Gmail, Grafana, Prometheus, and custom
  MCP servers. Add worker devices so jobs can run on other machines.
- **Reusable skills.** Use slash commands for common workflows or import a custom
  `SKILL.md`. Share files and images in desktop chat and generate images with a
  configured OpenAI API key.

Buddy is for people who want ongoing help across projects and daily life. A Bud
can help plan a day, maintain project notes, investigate a failing build, or keep
an eye on an inbox while longer work runs elsewhere.

## Screenshots

Captured from the running apps with fictional demo conversations, files, and jobs.
The iPhone screenshots are from the iOS Simulator.

### Desktop

Chat, quick decisions, files, and background work in one place.

![Buddy desktop conversation with three Buds, an inline choice card, and workspace activity](docs/screenshots/desktop-chat.png)

Inspect a job’s result, rendered code, and progress in Work.

![Buddy Work view showing a completed Go health endpoint task and another job in progress](docs/screenshots/desktop-work.png)

### iPhone

Connect to the same Buddy home to continue conversations and check work on the go.

<p>
  <img src="docs/screenshots/iphone-chats.png" width="270" alt="Buddy for iPhone showing Buds and group chats">
  <img src="docs/screenshots/iphone-chat.png" width="270" alt="Buddy for iPhone showing a group conversation with matching Bud avatars">
</p>

## Install and run

### macOS desktop

Install Git, Go **1.27.3 or later**, Xcode Command Line Tools, and the Codex CLI.
The default model is GPT-6.1 Terra, which requires Codex CLI **0.159.0 or later**.

```sh
git clone https://github.com/Hortlund/buddy.git
cd buddy
./script/build_and_run.sh
```

The script builds the desktop app and server, then opens Buddy. On first launch,
choose **Start on this Mac** or connect to an existing Buddy home. Configure the
model runtime in **Menu → Connections → Runtimes**.

For Codex CLI authentication, run `codex login` on the machine hosting the server.
An OpenAI API key can also be saved in Connections. Live voice and image generation
require API access configured for those capabilities.

The local server uses `https://127.0.0.1:7331`. Development state and logs live in
`.buddy/`, and build output goes to `dist/`. Both directories are ignored by Git.
Closing the app leaves the server running while the Mac is awake and signed in.

### Always-on server and other devices

Run the Go server on a Mac or Linux host that stays online, then connect clients
and worker devices to that home. Tailscale can provide a private route between
devices.

```sh
go build -o buddyd ./cmd/buddyd
```

Follow the guide to configure the host, model runtime, and client access.

- [Host a Buddy server](docs/guide.md#host-buddy-on-another-machine)
- [Connect the iPhone app](docs/guide.md#buddy-for-iphone)
- [Add worker devices](docs/guide.md#run-work-on-another-device)
- [Move an existing Buddy home](docs/guide.md#move-the-home-server)

### iPhone app

Open `ios/Buddy.xcodeproj` in Xcode and select the **Buddy** scheme. Run it on an
iOS 17+ simulator or sign it with a development team for a physical iPhone.
In desktop Connections, choose **Pair iPhone**, then scan the QR code or paste
the pairing link into the iPhone app.

## Documentation

The [setup and usage guide](docs/guide.md) covers first launch, runtime credentials,
integrations, voice calls, routines, memory, custom skills, MCP servers, and
deployment. It also documents current limits and optional configuration.

## Development and contributions

Bug reports, design feedback, and focused pull requests are welcome. For a bug,
include the app version, operating system, steps to reproduce, and relevant logs
with credentials and private conversation data removed.

Run the Go checks from the repository root.

```sh
go test ./...
go vet ./...
```

With the Codex CLI signed in, `python3 script/smoke_desktop.py` exercises the native
desktop against a temporary Buddy home. The hosting smoke test uses local Docker.
See the guide for details.

The main components are `cmd/buddy` for the Go/MyGo desktop, `cmd/buddyd` for the
server, `cmd/buddyworker` for remote work, and `ios/Buddy` for the SwiftUI client.

Keep API keys, OAuth downloads, connection files, server state, and backups out of
commits. The repository’s ignore rules cover common local files and generated
output.
