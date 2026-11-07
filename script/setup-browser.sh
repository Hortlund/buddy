#!/bin/sh
set -eu

# Provider installation only. Does not grant interaction access, edit personal
# model settings, or install root-owned operating system dependencies.
prefix=${BUDDY_TOOLS_DIR:-"$HOME/.local/share/buddy-tools"}
if [ "$#" -gt 0 ]; then
  if [ "$#" -ne 2 ] || [ "$1" != "--prefix" ]; then
    echo 'Usage: setup-browser.sh [--prefix /absolute/tools/path]' >&2
    exit 2
  fi
  prefix=$2
fi
case "$prefix" in /*) ;; *) echo 'Use an absolute tools path.' >&2; exit 2 ;; esac
command -v node >/dev/null 2>&1 || { echo 'Install Node.js 18+ first.' >&2; exit 1; }
command -v npm >/dev/null 2>&1 || { echo 'Install npm first.' >&2; exit 1; }
node -e 'if (Number(process.versions.node.split(".")[0]) < 18) process.exit(1)' || {
  echo 'Node.js 18+ is required.' >&2; exit 1;
}
umask 077
mkdir -p "$prefix"
npm install --prefix "$prefix" --no-audit --no-fund @playwright/mcp@0.0.84
node "$prefix/node_modules/playwright/cli.js" install chromium
echo
echo 'Browser installed. Use this tools directory in your server or worker PATH:'
echo "$prefix/node_modules/.bin"
echo 'Then enable Browser in Connections, or stop the worker and run:'
echo 'buddyworker setup-automation -config /private/worker/execution.json -provider playwright'
echo 'Restart the worker with that PATH. No model request was made.'
echo 'If Linux reports missing shared libraries, an administrator can run:'
echo "node \"$prefix/node_modules/playwright/cli.js\" install-deps chromium"
