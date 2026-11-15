#!/usr/bin/env python3
"""Small, dependency-free release tooling, also installed on hosted servers."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shutil
import sqlite3
import tarfile
import tempfile
import urllib.parse
import urllib.request

REPOSITORY = 'Hortlund/buddy'
VERSION = re.compile(r'v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\Z')
SERVER_FILES = ('buddyd', 'buddyworker', 'buddy-server.sh', 'release.py')
MAX_ARCHIVE = 256 * 1024 * 1024


def tag(value):
    if not VERSION.fullmatch(value):
        raise ValueError('Use a stable version such as v0.2.0.')
    return 'v' + value.removeprefix('v')


class HTTPSRedirects(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, fp, code, msg, headers, new_url):
        if urllib.parse.urlsplit(new_url).scheme != 'https':
            raise ValueError('Refusing an insecure download redirect.')
        return super().redirect_request(request, fp, code, msg, headers, new_url)


def fetch(url, output, limit):
    if urllib.parse.urlsplit(url).scheme != 'https':
        raise ValueError('Downloads require HTTPS.')
    request = urllib.request.Request(url, headers={'User-Agent': 'Buddy-releases', 'Accept': 'application/vnd.github+json' if 'api.github.com/' in url else '*/*'})
    opener = urllib.request.build_opener(HTTPSRedirects)
    with opener.open(request, timeout=60) as response, open(output, 'wb') as target:
        total = 0
        while chunk := response.read(1024 * 1024):
            total += len(chunk)
            if total > limit:
                raise ValueError('Release download exceeds the size limit.')
            target.write(chunk)


def latest():
    with tempfile.TemporaryDirectory() as scratch:
        path = Path(scratch) / 'release.json'
        fetch(f'https://api.github.com/repos/{REPOSITORY}/releases/latest', path, 2 * 1024 * 1024)
        release = json.loads(path.read_text())
        if release.get('draft') or release.get('prerelease'):
            raise ValueError('The latest release is not stable.')
        return tag(release['tag_name'])


def verify_archive(archive, checksums, name):
    matches = []
    for line in checksums.splitlines():
        fields = line.split()
        if len(fields) == 2 and fields[1].lstrip('*') == name:
            matches.append(fields[0])
    if len(matches) != 1 or not re.fullmatch('[a-f0-9]{64}', matches[0]):
        raise ValueError(f'Missing or ambiguous SHA-256 for {name}.')
    with open(archive, 'rb') as source:
        digest = hashlib.sha256()
        while chunk := source.read(1024 * 1024):
            digest.update(chunk)
        actual = digest.hexdigest()
    if actual != matches[0]:
        raise ValueError(f'Checksum failed for {name}; the running server has not been changed.')


def extract_server(archive, output):
    # Never let tar create paths, links, devices or permissions from the archive.
    with tarfile.open(archive, 'r:gz') as bundle:
        members = bundle.getmembers()
        if len(members) != len(SERVER_FILES) or {m.name for m in members} != set(SERVER_FILES):
            raise ValueError('Unexpected files in the server release.')
        if any(not m.isfile() or m.size <= 0 for m in members) or sum(m.size for m in members) > MAX_ARCHIVE:
            raise ValueError('Invalid server release contents.')
        output.mkdir(mode=0o700, parents=True, exist_ok=False)
        for member in members:
            with bundle.extractfile(member) as source, open(output / member.name, 'xb') as target:
                shutil.copyfileobj(source, target)
            (output / member.name).chmod(0o700)


def download_server(output, version='', host_os=None, arch=None):
    version = tag(version) if version else latest()
    host_os = host_os or platform.system().lower()
    arch = arch or {'x86_64': 'amd64', 'aarch64': 'arm64', 'arm64': 'arm64'}.get(platform.machine())
    if host_os not in ('linux', 'darwin') or arch not in ('amd64', 'arm64'):
        raise ValueError('Supported server platforms: Linux/macOS on amd64/arm64.')
    name = f'buddy-server_{version}_{host_os}_{arch}.tar.gz'
    prefix = f'https://github.com/{REPOSITORY}/releases/download/{version}/'
    with tempfile.TemporaryDirectory() as scratch:
        archive, sums = Path(scratch) / name, Path(scratch) / 'checksums.txt'
        fetch(prefix + 'checksums.txt', sums, 1024 * 1024)
        fetch(prefix + name, archive, MAX_ARCHIVE)
        verify_archive(archive, sums.read_text(), name)
        extract_server(archive, Path(output))
    return version.removeprefix('v')


def snapshot(state, output):
    """Take an exact recovery copy, including phone pairings, without migrations."""
    state, output = Path(state).resolve(), Path(output).resolve()
    if output == state or state in output.parents:
        raise ValueError('Recovery copies must be outside the server state directory.')
    # Same kernel lock as buddyd; snapshotting must never race a live daemon.
    import fcntl
    with open(state / 'server.lock', 'r+b') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        output.mkdir(mode=0o700, parents=True, exist_ok=False)
        try:
            uri = (state / 'buddy.db').as_uri() + '?mode=ro'
            with sqlite3.connect(uri, uri=True) as source, sqlite3.connect(output / 'buddy.db') as target:
                source.backup(target)
                if target.execute('PRAGMA quick_check').fetchone() != ('ok',):
                    raise ValueError('The pre-update database failed its integrity check.')
            for item in state.iterdir():
                if item.name.startswith('.update'):
                    continue
                if item.name in ('buddy.db', 'buddy.db-wal', 'buddy.db-shm', 'server.lock'):
                    continue
                if item.is_symlink():
                    raise ValueError(f'Cannot back up a symlink in server state: {item.name}')
                if item.is_dir():
                    shutil.copytree(item, output / item.name, symlinks=True)
                elif item.is_file():
                    shutil.copy2(item, output / item.name)
        except BaseException:
            shutil.rmtree(output)
            raise


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest='command', required=True)
    download = commands.add_parser('download-server')
    download.add_argument('--output', required=True)
    download.add_argument('--version', default='')
    backup = commands.add_parser('snapshot')
    backup.add_argument('--state', required=True)
    backup.add_argument('--output', required=True)
    validate = commands.add_parser('validate-version')
    validate.add_argument('version')
    commands.add_parser('latest-version')
    args = parser.parse_args()
    if args.command == 'download-server':
        print(download_server(args.output, args.version))
    elif args.command == 'snapshot':
        snapshot(args.state, args.output)
    elif args.command == 'latest-version':
        print(latest().removeprefix('v'))
    else:
        print(tag(args.version).removeprefix('v'))


if __name__ == '__main__':
    os.umask(0o077)
    try:
        main()
    except (ValueError, OSError, sqlite3.Error, tarfile.TarError) as error:
        raise SystemExit(f'Buddy release: {error}')
