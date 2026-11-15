#!/usr/bin/env bash
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"
version=$(python3 script/release.py validate-version "${BUDDY_VERSION:?Set BUDDY_VERSION}")
host_os=${GOOS:-$(go env GOOS)}
arch=${GOARCH:-$(go env GOARCH)}
destination=${BUDDY_RELEASE_DIR:-$ROOT_DIR/dist/releases}
mkdir -p "$destination"
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
commit=${BUDDY_COMMIT:-$(git rev-parse HEAD)}
build_date=${BUDDY_BUILD_DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}
ldflags="-s -w -X buddy/internal/buildinfo.Version=$version -X buddy/internal/buildinfo.Commit=$commit -X buddy/internal/buildinfo.Date=$build_date"
for command in buddyd buddyworker; do
  CGO_ENABLED=0 GOOS="$host_os" GOARCH="$arch" go build -trimpath -ldflags "$ldflags" -o "$stage/$command" "./cmd/$command"
done
cp script/buddy-server.sh script/release.py "$stage/"
# Python produces the same archive shape on macOS and Linux, without Apple's
# resource fork files or extended attributes that would fail strict extraction.
python3 - "$stage" "$destination/buddy-server_v${version}_${host_os}_${arch}.tar.gz" <<'PY'
from pathlib import Path
import sys, tarfile
with tarfile.open(sys.argv[2], 'w:gz') as archive:
    for name in ('buddyd', 'buddyworker', 'buddy-server.sh', 'release.py'):
        archive.add(Path(sys.argv[1]) / name, arcname=name, recursive=False)
PY
