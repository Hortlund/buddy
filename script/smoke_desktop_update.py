#!/usr/bin/env python3
"""Exercise MyGo's real verifier and deferred bundle installer on a disposable Mac app."""
import json
import os
from pathlib import Path
import plistlib
import subprocess
import sys
import tarfile
import tempfile
import time

ROOT = Path(__file__).resolve().parents[1]
FIXTURE = r'''package main
import (
    "context"
    "crypto/ed25519"
    "encoding/base64"
    "encoding/json"
    "fmt"
    "io"
    "os"
    "github.com/egoist/mygo"
)
type source struct { manifest, archive string }
func (s source) Manifest(context.Context, bool) (io.ReadCloser, error) { return os.Open(s.manifest) }
func (s source) Open(context.Context, string) (io.ReadCloser, error) { return os.Open(s.archive) }
func main() {
    if os.Args[1] == "keys" {
        pub, priv, err := ed25519.GenerateKey(nil); if err != nil { panic(err) }
        json.NewEncoder(os.Stdout).Encode(map[string]string{"public":base64.StdEncoding.EncodeToString(pub),"private":base64.StdEncoding.EncodeToString(priv)})
        return
    }
    mygo.App.SetVersion("0.1.0")
    mygo.Updater.SetSource(source{os.Args[1], os.Args[2]})
    if !mygo.Updater.Enabled() { panic("MyGo updater metadata is not enabled") }
    update, err := mygo.Updater.Check(context.Background())
    if err != nil || update == nil || update.Version != "0.2.0" { panic(fmt.Sprintf("check: %v %v", update, err)) }
    err = update.Install(context.Background(), nil)
    if len(os.Args) > 3 && os.Args[3] == "reject" {
        if err == nil { panic("accepted a corrupted update") }
        fmt.Println("Rejected corrupted update")
        return
    }
    if err != nil { panic(err) }
    fmt.Println("Staged verified update")
}
'''


def run(*args, **kwargs):
    return subprocess.run(args, cwd=ROOT, check=True, **kwargs)


def main():
    if sys.platform != 'darwin':
        raise SystemExit('Run the desktop update smoke test on macOS.')
    with tempfile.TemporaryDirectory(prefix='buddy-update-') as temporary:
        folder = Path(temporary)
        fixture = folder / 'updater.go'
        fixture.write_text(FIXTURE)
        keys = json.loads(run('go', 'run', fixture, 'keys', capture_output=True, text=True).stdout)
        env = dict(os.environ, BUDDY_DIST_DIR=str(folder / 'candidate'), BUDDY_VERSION='0.2.0',
                   BUDDY_BUILD_NUMBER='0.2.0', BUDDY_UNIVERSAL='1', BUDDY_UPDATE_PUBLIC_KEY=keys['public'],
                   MYGO_UPDATER_PRIVATE_KEY=keys['private'], BUDDY_UPDATE_FEED='https://github.com/Hortlund/buddy/releases/latest/download/update-darwin-universal.json')
        run('bash', 'script/package-desktop.sh', env=env)
        candidate = folder / 'candidate' / 'Buddy.app'
        archive, manifest = folder / 'buddy-0.2.0-darwin-universal.tar.gz', folder / 'update-darwin-universal.json'
        with tarfile.open(archive, 'w:gz') as bundle:
            bundle.add(candidate, arcname='Buddy.app')
        run('go', 'run', './script/sign-update.go', archive, manifest, '0.2.0', env=env)
        installed = folder / 'installed' / 'Buddy.app'
        executable = installed / 'Contents' / 'MacOS' / 'BuddyDesktop'
        executable.parent.mkdir(parents=True)
        (installed / 'old-version').write_text('0.1.0')
        flags = '-X github.com/egoist/mygo.production=1 -X github.com/egoist/mygo.packageUpdateFeed=https://example.com/update.json -X github.com/egoist/mygo.packageUpdateKey=' + keys['public']
        run('go', 'build', '-ldflags', flags, '-o', executable, fixture)
        corrupted = folder / 'corrupted.tar.gz'
        content = bytearray(archive.read_bytes())
        content[-1] ^= 1
        corrupted.write_bytes(content)
        run(executable, manifest, corrupted, 'reject')
        assert (installed / 'old-version').exists(), 'corrupt update replaced the installed app'
        run(executable, manifest, archive)
        for _ in range(100):
            info = installed / 'Contents' / 'Info.plist'
            if info.exists() and not (installed / 'old-version').exists():
                with info.open('rb') as source:
                    assert plistlib.load(source)['CFBundleShortVersionString'] == '0.2.0'
                break
            time.sleep(.05)
        else:
            raise AssertionError('MyGo did not replace the exited app with the staged update')
        helper = installed / 'Contents' / 'Helpers' / 'buddyd'
        assert run(helper, '-version', capture_output=True, text=True).stdout.strip() == '0.2.0'
        run('codesign', '--verify', '--deep', '--strict', installed)
        architectures = run('lipo', '-archs', executable, capture_output=True, text=True).stdout.split()
        assert set(architectures) == {'arm64', 'x86_64'}, architectures
        print('Verified: MyGo signed manifest, corruption rejection, bundle replacement after exit, code signature, universal desktop, and bundled server.')


if __name__ == '__main__':
    main()
