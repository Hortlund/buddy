import contextlib
import fcntl
import hashlib
import importlib.util
import io
import os
from pathlib import Path
import sqlite3
import subprocess
import tarfile
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('release', Path(__file__).with_name('release.py'))
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)


class ReleaseTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)

    def archive(self, members=None):
        path = self.root / 'server.tar.gz'
        with tarfile.open(path, 'w:gz') as bundle:
            for name, kind in members or [(name, tarfile.REGTYPE) for name in release.SERVER_FILES]:
                member = tarfile.TarInfo(name)
                member.type = kind
                member.linkname = '/etc/passwd' if kind == tarfile.SYMTYPE else ''
                member.size = 3 if kind == tarfile.REGTYPE else 0
                bundle.addfile(member, io.BytesIO(b'bin') if member.size else None)
        return path

    def test_checksum_rejects_corruption_and_duplicate_entries(self):
        path = self.archive()
        digest = hashlib.sha256(path.read_bytes()).hexdigest()
        line = f'{digest}  server.tar.gz\n'
        release.verify_archive(path, line, path.name)
        for sums in ('0' * 64 + '  server.tar.gz', line + line, ''):
            with self.assertRaises(ValueError):
                release.verify_archive(path, sums, path.name)

    def test_archive_extracts_only_the_expected_regular_files(self):
        destination = self.root / 'output'
        release.extract_server(self.archive(), destination)
        self.assertEqual(set(release.SERVER_FILES), {p.name for p in destination.iterdir()})
        self.assertTrue(all(p.stat().st_mode & 0o777 == 0o700 for p in destination.iterdir()))

    def test_archive_rejects_paths_links_duplicates_and_extra_files(self):
        originals = [(name, tarfile.REGTYPE) for name in release.SERVER_FILES]
        for replacement in ('../buddyd', '/buddyd', 'folder/buddyd', 'buddyworker'):
            with self.subTest(path=replacement), self.assertRaises(ValueError):
                release.extract_server(self.archive([(replacement, tarfile.REGTYPE)] + originals[1:]), self.root / 'output')
        with self.assertRaises(ValueError):
            release.extract_server(self.archive([('buddyd', tarfile.SYMTYPE)] + originals[1:]), self.root / 'output')
        with self.assertRaises(ValueError):
            release.extract_server(self.archive(originals + [('extra', tarfile.REGTYPE)]), self.root / 'output')
        self.assertFalse((self.root / 'output').exists())

    def test_stable_versions_only(self):
        self.assertEqual(release.tag('1.2.3'), 'v1.2.3')
        for value in ('v01.2.3', 'v1.2', 'v1.2.3-rc.1', 'main', '../v1.2.3', 'v1.2.3\n'):
            with self.subTest(value=value), self.assertRaises(ValueError):
                release.tag(value)

    def test_download_refuses_http(self):
        with self.assertRaises(ValueError):
            release.fetch('http://example.com/file', self.root / 'file', 100)

    def test_snapshot_requires_stopped_daemon_and_preserves_exact_pairings(self):
        state = self.root / 'state'
        state.mkdir(mode=0o700)
        (state / 'server.lock').touch()
        (state / 'server.token').write_text('same-token')
        (state / 'profiles').mkdir()
        (state / 'profiles' / 'bud.md').write_text('same-profile')
        (state / '.update-lock').mkdir()
        connection = sqlite3.connect(state / 'buddy.db')
        self.addCleanup(connection.close)
        connection.execute('PRAGMA journal_mode=WAL')
        connection.execute('CREATE TABLE mobile_clients (id TEXT)')
        connection.execute("INSERT INTO mobile_clients VALUES ('keep-pairing')")
        connection.commit()
        with open(state / 'server.lock', 'r+b') as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            with self.assertRaises(BlockingIOError):
                release.snapshot(state, self.root / 'busy-backup')
        self.assertFalse((self.root / 'busy-backup').exists())
        backup = self.root / 'backup'
        release.snapshot(state, backup)
        with sqlite3.connect(backup / 'buddy.db') as copied:
            self.assertEqual(copied.execute('SELECT * FROM mobile_clients').fetchall(), [('keep-pairing',)])
        self.assertEqual((backup / 'server.token').read_text(), 'same-token')
        self.assertEqual((backup / 'profiles' / 'bud.md').read_text(), 'same-profile')
        self.assertFalse((backup / '.update-lock').exists())


class UpdateCommandTests(unittest.TestCase):
    """Exercise the real Bash updater with isolated downloads and service manager."""
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.home, self.install, self.bin = [self.root / name for name in ('home', 'install', 'bin')]
        for directory in (self.home / 'state', self.install, self.bin):
            directory.mkdir(parents=True)
        self.events = self.root / 'events'
        self.write(self.install / 'buddyd', '#!/bin/bash\necho 0.1.0\n')
        # This fixture replaces network and migration operations only; the real
        # shell controls sequencing, versions, staging, copying and cleanup.
        self.write(self.install / 'release.py', '''import os, pathlib, sys
args = sys.argv[1:]
if args[0] == 'latest-version': print('0.2.0')
elif args[0] == 'validate-version': print(args[1].removeprefix('v'))
elif args[0] == 'snapshot':
    out = pathlib.Path(args[args.index('--output') + 1]); out.mkdir(parents=True)
    (out/'buddy.db').write_text('recovery database')
elif args[0] == 'download-server':
    if os.environ.get('FIXTURE_CORRUPT'): sys.exit('Checksum failed')
    out = pathlib.Path(args[args.index('--output') + 1]); out.mkdir()
    for name in ('buddyd', 'buddyworker'):
        (out/name).write_text("""#!/bin/bash
if [ "$1" = -version ]; then echo 0.2.0
elif [[ "$*" == *-stop-idle-pid* ]]; then
  if [ "${FIXTURE_BUSY:-}" = 1 ]; then echo 'active or queued work' >&2; exit 1; fi
  if [ "${FIXTURE_STOP_FAIL:-}" = 1 ]; then echo 'SIGTERM sent; shutdown wait failed' >&2; exit 3; fi
elif [[ "$*" == *-stop-idle-service* ]]; then
  echo 'waited for old service to stop' >> "$FIXTURE_EVENTS"
fi
""")
        (out/name).chmod(0o700)
    (out/'buddy-server.sh').write_text('# next manager')
    (out/'release.py').write_text('# next release tools')
    print('0.2.0')
''')
        self.write(self.bin / 'launchctl', '''#!/bin/bash
printf '%s\\n' "$*" >> "$FIXTURE_EVENTS"
if [ "$1" = print ]; then echo '    pid = 4321'; fi
if [ "$1" = bootstrap ] && [ "${FIXTURE_RESTART_FAIL:-}" = 1 ]; then exit 1; fi
''')
        self.write(self.bin / 'systemctl', '''#!/bin/bash
echo "systemctl $*" >> "$FIXTURE_EVENTS"
if [ "$1" = show ]; then echo 4321; fi
if [ "$1" = restart ] && [ "${FIXTURE_RESTART_FAIL:-}" = 1 ]; then exit 1; fi
exit 0
''')
        self.write(self.bin / 'sudo', '#!/bin/bash\nexec "$@"\n')
        self.write(self.bin / 'python3', '''#!/bin/bash
if [ "$1" = - ] && [ "$#" = 5 ]; then
  cat >/dev/null
  [ "${FIXTURE_HEALTH_FAIL:-}" != 1 ]
else exec "$FIXTURE_PYTHON" "$@"; fi
''')
        self.write(self.bin / 'sleep', '#!/bin/bash\nexit 0\n')
        self.wrapper = self.root / 'buddy-server'
        source = Path(__file__).with_name('buddy-server.sh').resolve()
        import shlex
        self.write(self.wrapper, '#!/bin/bash\n' + '\n'.join(f'{key}={shlex.quote(value)}' for key, value in {
            'host_os': 'darwin', 'service_user': os.environ.get('USER', ''), 'service_home': str(self.home),
            'install_dir': str(self.install), 'runtime_path': str(self.bin) + ':' + os.environ['PATH'], 'server_port': '7331'
        }.items()) + f'\nhost_os=${{FIXTURE_HOST_OS:-darwin}}\nsource {shlex.quote(str(source))}\n')

    def write(self, path, content):
        path.write_text(content)
        path.chmod(0o700)

    def run_update(self, version=None, **settings):
        import sys
        env = dict(os.environ, PATH=str(self.bin) + ':' + os.environ['PATH'], FIXTURE_EVENTS=str(self.events), FIXTURE_PYTHON=sys.executable, **settings)
        return subprocess.run([str(self.wrapper), 'update'] + ([version] if version else []), env=env, capture_output=True, text=True)

    def test_failed_download_does_not_stop_or_change_server(self):
        result = self.run_update(FIXTURE_CORRUPT='1')
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertFalse(self.events.exists())
        self.assertIn('0.1.0', (self.install / 'buddyd').read_text())
        self.assertFalse((self.home / '.update-lock').exists())

    def test_busy_queue_does_not_stop_or_replace_server(self):
        result = self.run_update(FIXTURE_BUSY='1')
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('active or queued work', result.stderr)
        self.assertNotIn('kill', self.events.read_text())
        self.assertNotIn('bootstrap', self.events.read_text())
        self.assertIn('0.1.0', (self.install / 'buddyd').read_text())
        self.assertFalse((self.home / 'state' / '.update-lock').exists())

    def test_post_signal_failure_recovers_previous_mac_service(self):
        result = self.run_update(FIXTURE_STOP_FAIL='1')
        self.assertEqual(result.returncode, 3, result.stdout + result.stderr)
        events = self.events.read_text()
        self.assertLess(events.index('waited for old service to stop'), events.index('bootstrap'))
        self.assertIn('0.1.0', (self.install / 'buddyd').read_text())
        self.assertFalse((self.home / 'state' / '.update-lock').exists())
        self.assertFalse((self.home / '.update-lock').exists())

    def test_post_signal_failure_recovers_previous_linux_service(self):
        result = self.run_update(FIXTURE_STOP_FAIL='1', FIXTURE_HOST_OS='linux')
        self.assertEqual(result.returncode, 3, result.stdout + result.stderr)
        self.assertIn('systemctl restart buddyd.service', self.events.read_text())
        self.assertIn('0.1.0', (self.install / 'buddyd').read_text())
        self.assertFalse((self.home / 'state' / '.update-lock').exists())

    def test_failed_old_service_recovery_is_reported(self):
        result = self.run_update(FIXTURE_STOP_FAIL='1', FIXTURE_HOST_OS='linux', FIXTURE_RESTART_FAIL='1')
        self.assertEqual(result.returncode, 3, result.stdout + result.stderr)
        self.assertIn('service could not be restarted', result.stderr)
        self.assertIn('buddy-server start', result.stderr)
        self.assertIn('0.1.0', (self.install / 'buddyd').read_text())

    def test_success_retains_recovery_and_releases_maintenance(self):
        result = self.run_update()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('0.2.0 is ready', result.stdout)
        self.assertIn('0.1.0', (self.install / 'previous' / 'buddyd').read_text())
        self.assertEqual(len(list((self.home / '.buddy-migration' / 'updates').glob('*/buddy.db'))), 1)
        self.assertFalse((self.home / 'state' / '.update-lock').exists())

    def test_failed_start_stays_stopped_and_preserves_maintenance(self):
        result = self.run_update(FIXTURE_HEALTH_FAIL='1')
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('Service stopped to preserve recovery data', result.stderr)
        self.assertTrue((self.home / 'state' / '.update-lock').exists())
        self.assertTrue((self.install / 'previous' / 'buddyd').exists())
        self.assertFalse((self.home / '.update-lock').exists())

    def test_equal_version_does_not_download_or_restart(self):
        result = self.run_update('0.1.0', FIXTURE_CORRUPT='1')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('already installed', result.stdout)
        self.assertFalse(self.events.exists())

    def test_downgrade_does_not_restart(self):
        result = self.run_update('0.0.9')
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('Refusing a downgrade', result.stderr)
        self.assertFalse(self.events.exists())


if __name__ == '__main__':
    unittest.main()
