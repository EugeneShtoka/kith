#!/usr/bin/env bash
#
# desktop-check.sh — the desktop's gates, as CI runs them (make desktop-check):
#
#   1. install from the lock (npm ci), only when the lock changed since the last install:
#      no dependency's install script runs (desktop/.npmrc) and Electron's binary is
#      not fetched (ELECTRON_SKIP_BINARY_DOWNLOAD), since no gate here runs Electron;
#   2. typecheck (tsc, strict, the main and window halves apart), lint (ESLint, with the
#      layer rules), format (Prettier), unit tests (vitest);
#   3. electron-forge package: the bundles build and package, fuses set;
#   4. npm audit of the runtime tree, what the package ships (scripts/npm-audit.sh).

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT/desktop"

if ! command -v npm >/dev/null 2>&1 || ! npm --version >/dev/null 2>&1; then
	echo "desktop-check: needs node and npm (https://nodejs.org; CI's is node 24, desktop/.nvmrc)" >&2
	exit 1
fi

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi
}

lock=$(sha256 package-lock.json)
stamp=node_modules/.kith-lock
if [[ ! -f $stamp || "$(cat "$stamp")" != "$lock" ]]; then
	echo "desktop-check: installing from the lock" >&2
	ELECTRON_SKIP_BINARY_DOWNLOAD=1 npm ci --no-audit --no-fund
	echo "$lock" >"$stamp"
fi

npm run --silent typecheck
npm run --silent lint
npm run --silent format:check
npm run --silent test
npm run --silent package
bash "$ROOT/scripts/npm-audit.sh" "$ROOT/desktop"
echo "desktop-check: typecheck, lint, format, tests, package and the runtime audit pass ✓"
