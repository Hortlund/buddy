# Releases and updates

Buddy keeps the Go/MyGo desktop, hosted server, and workers independently
updatable. GitHub Releases hosts the immutable downloads, SHA-256 checksums, and
the MyGo desktop feed. No additional update service or database is needed.
The desktop remains a MyGo app; its built-in native updater plugin handles the
signed update flow with no separate framework or native bridge. iPhone
distribution is separate from this workflow.

## Installing the desktop

Download [Buddy.dmg](https://github.com/Hortlund/buddy/releases/latest/download/Buddy.dmg),
open it, and drag Buddy onto Applications. Eject the disk image before opening
Buddy from Applications, so it can save and install future MyGo updates.
For a manual reinstall, quit Buddy before replacing the app. The same download
works on Intel and Apple Silicon Macs running macOS 12 or newer.

The app includes the local server. First launch lets you start on this Mac or
connect to a separately hosted home; model runtime setup remains on its host.
The DMG needs no installer scripts or additional installer signing certificate.
The download becomes available when the first signed release is published.

## Updating an installation

| Installation | Update |
| --- | --- |
| Release desktop on macOS | **Settings → About → Check for updates…**; MyGo also offers automatic checks |
| Desktop-managed local server | Update the desktop, then select **Update local server** in Settings or Connections |
| Installer-managed Linux or Mac server | Run `buddy-server update` **on that host**, over SSH if needed |
| Worker on another machine | Replace `buddyworker` from that release's matching server archive and restart with the existing arguments and connection file |
| Development checkout | Continue using `./script/build_and_run.sh` |

`buddy-server update v0.2.0` selects a particular newer stable release. A routine
update refuses downgrades, and an already-installed version returns immediately
without downloading binaries or restarting. Servers need outbound HTTPS to
GitHub and its release asset hosts; desktops do not need SSH credentials or
permission to modify a remote server.

The update keeps the listen address, Tailscale route, TLS identity, owner access,
phone pairings, model sign-in, service settings, profiles, and managed workspaces.
The desktop reconnects to the same server after the restart. Update the server
before the desktop and workers when deploying a release to several machines.
Existing protocol checks continue to hide features unavailable on older servers.
Local and remote servers are never replaced merely by updating a desktop.

Servers installed before this flow need one rerun of the installer to refresh
their management tools. This keeps their running daemon and existing state.
Then use `buddy-server update`. A custom, unmanaged service should use the
published binaries with its own stop, backup, and restart procedure.

## What a hosted update does

1. Resolve a stable release and download the correct Linux/macOS, amd64/arm64
   archive. Require HTTPS, bound download size, verify SHA-256 from that
   release's checksum file, reject unexpected archive members, and check the
   executable versions. The current service remains available during downloads.
2. Reserve the SQLite write queue and refuse to stop a daemon with running,
   cancelling, or queued tasks. A graceful stop holds that reservation until
   the old daemon releases its data-directory lock. Finish pending work and
   live calls before updating; retry if Buddy reports a busy queue. If SIGTERM
   was sent but the shutdown wait fails, the updater restarts the unchanged old
   service and reports the failure. A busy queue leaves the service untouched.
3. Create a private database and state recovery copy under
   `$SERVICE_HOME/.buddy-migration/updates/`. Unlike export snapshots, this exact
   database copy retains existing phone pairings. Validate the new schema on a
   disposable copy of that database before changing installed binaries.
4. Retain the previous binaries under `$INSTALL_DIR/previous`, install with
   atomic file replacements, and restart the existing service. The candidate
   answers liveness checks but blocks client operations, jobs, and routines.
   The updater verifies its version and checks its database through the
   owner-authenticated `/v1/maintenance/ready` endpoint using the saved TLS
   certificate. Public `/readyz` returns 503 until maintenance ends.
5. Remove the maintenance marker to resume normal operation. If startup fails,
   stop the candidate, retain the recovery copy and maintenance marker, and
   report their paths. A reboot cannot silently run jobs on a failed candidate;
   public readiness stays unavailable while that marker remains.

Linux uses `/opt/buddy` and `/var/lib/buddy`; hosted macOS uses
`~/.local/share/buddy-server/bin` and `~/.local/share/buddy-server`. These updates
do not upgrade Codex or change service definitions. Runtime/toolchain upgrades
should be separately tested changes. Prebuilt servers do not require Go to
install or update; install Go separately if project work needs it.

Recovery copies contain credentials and are deliberately retained rather than
deleted automatically. After verifying the new version and taking an off-host
backup, an administrator can remove obsolete timestamped recovery copies. Keep
enough free space for an archive, the staged binaries, and a full state copy;
managed workspace files are not duplicated by routine updates.

## One-time release configuration

Create a GitHub environment named `release` for this repository. Configure these
environment secrets; the workflow fails before publishing when any are absent.

| Secret | Value |
| --- | --- |
| `APPLE_CERTIFICATE_P12` | Base64 of an exported **Developer ID Application** certificate and private key |
| `APPLE_CERTIFICATE_PASSWORD` | Password protecting that P12 |
| `APPLE_SIGN_IDENTITY` | Full `Developer ID Application: … (TEAMID)` signing identity |
| `APPLE_ID` | Apple developer account used for notarization |
| `APPLE_TEAM_ID` | Apple Developer team ID |
| `APPLE_APP_PASSWORD` | App-specific password for notarization |
| `MYGO_UPDATER_PRIVATE_KEY` | Contents of the `mygo-update.key` file from MyGo keygen |

Set the `MYGO_UPDATE_PUBLIC_KEY` **GitHub environment variable** to the contents
of the matching `mygo-update.pub`. Create the pair once using the pinned MyGo CLI:

```sh
go run github.com/egoist/mygo/cmd/mygo@v0.9.1 keygen -o /private/path/buddy-update-keys
```

Store the private file's contents as `MYGO_UPDATER_PRIVATE_KEY` and the public
file's contents as `MYGO_UPDATE_PUBLIC_KEY`. Keep an offline recovery copy of the
key. Maintain the same bundle ID, signing team, and update key across releases.
The MyGo version is already pinned in `go.mod`.

The default feed is
`https://github.com/Hortlund/buddy/releases/latest/download/update-darwin-universal.json`.
It points to a signed archive at an immutable release tag. Only stable tags enter
this feed. Buddy's existing bundler sets the same update metadata as the pinned
MyGo CLI and writes its documented JSON manifest; MyGo's built-in updater owns
verification, extraction, installation, preferences, and relaunching.

## Publishing

Merge the release changes into `main`, then push a new, increasing stable tag:

```sh
git tag -a v0.2.0 -m "Buddy v0.2.0"
git push origin v0.2.0
```

The release workflow verifies the tag is on `main` and newer than published
stable releases, then runs the same CI as pull requests. CI checks formatting,
vet, race tests on Linux and macOS, release/update tests, a desktop package, and
a Linux installer smoke test. Release jobs build:

- `Buddy.dmg`: universal desktop with a drag-to-Applications link and install
  instructions; signed with Developer ID, notarized, and stapled.
- `Buddy_vX.Y.Z_universal.zip`: Intel + Apple Silicon desktop and bundled server,
  signed with Developer ID, notarized, stapled, with the matching signed archive for MyGo updates.
- `buddy-server_vX.Y.Z_{linux,darwin}_{amd64,arm64}.tar.gz`: static Go server and
  worker binaries plus installed management tools.
- `buddy-X.Y.Z-darwin-universal.tar.gz`, `update-darwin-universal.json`,
  `checksums.txt`, and `install-server.sh`.

The release starts as a draft only after every build succeeds. All assets upload
before it is made public and marked latest, so a failed build/upload cannot
replace the public update feed with an incomplete release. A failed publish can
be rerun against its draft. Published tags and assets must not be rewritten;
fix a released issue with a higher version. Local unsigned packages remain
available through `script/package-desktop.sh`, without touching running apps or
services. To create a local disk image on macOS:

```sh
./script/package-desktop.sh
./script/package-installer.sh
```

This writes `dist/releases/Buddy.dmg`. Local builds use ad-hoc app signing by
default and are for testing on the build Mac. Production releases use the
configured Developer ID Application identity to sign both app and DMG, submit
the DMG once to Apple, then staple the image and the original app. ZIP and MyGo
update archives are made from that stapled app. This follows MyGo's distribution
flow and uses the same signing credentials already required for desktop releases.

CI exercises MyGo's real signature verifier and bundle installer with disposable
keys, including corruption rejection and the bundled server, using
`python3 script/smoke_desktop_update.py`. It also builds and mounts the read-only
DMG, copies Buddy into a temporary Applications folder, and verifies the copied
app's signature, bundled server, and access permissions. The Linux smoke test
exercises an actual server release update and chat after the restart. Apple signing and notarization
require real account credentials. Before the first public release, test two
Developer ID builds and relaunch on a clean Mac using the production feed.
Verify phone access and worker reconnection against a disposable hosted home.

## Recovering an interrupted or failed hosted update

Read the reported backup path and service logs first. Do not rerun the installer
or remove state. Stop the service and ensure no `buddyd` process still holds
`state/server.lock` before restoring anything. A database migration is why a
binary-only downgrade is insufficient.

For Linux, use `sudo systemctl stop buddyd.service`. For a hosted Mac, use
`launchctl bootout gui/$(id -u)/dev.buddy.hosted`. Keep the failed state in a
separate private folder for diagnosis. Restore the saved state files into the
existing state directory, preserving ownership and mode 0700 on directories and
0600 on credentials. Remove `buddy.db-wal` and `buddy.db-shm` from the failed state
before replacing its database; never combine an old database with a newer WAL.
Keep the original `server.lock` file/inode, which is intentionally absent from
the recovery copy. Restore the previous executable set from
`$INSTALL_DIR/previous`. Keep the existing management wrapper and service settings.

When restoring to the old version, remove `state/.update-lock` after the restore
is complete. Also remove `$SERVICE_HOME/.update-lock` if an interrupted updater
left it, after verifying that updater is no longer running. Start the existing
Linux service, or bootstrap the hosted Mac's existing
`~/Library/LaunchAgents/dev.buddy.hosted.plist`. Run `buddy-server status` and
verify desktop, phone, and worker access. Recovery returns Buddy to the saved
pre-update state; work after that point is not part of that copy.
