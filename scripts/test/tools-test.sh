#!/usr/bin/env bash
# Tests for the scripts that keep `make check` and CI the same: ensure-tools.sh and
# ci-parity-check.sh. Mostly the ways they must fail: a tampered archive, a missing
# download, a gate on one side only, a tool called around its make target.
#
#   bash scripts/test/tools-test.sh   (make script-check; offline: file:// downloads)
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
failures=0
ran=0

pass() { ran=$((ran + 1)); }
fail() {
	ran=$((ran + 1))
	failures=$((failures + 1))
	echo "FAIL: $*" >&2
}

# expect <want-exit> <want-output-substring or ""> <name> -- command…
expect() {
	local want=$1 needle=$2 name=$3
	shift 4
	local out code
	out=$("$@" 2>&1)
	code=$?
	if [[ "$code" != "$want" ]]; then
		fail "$name: exit $code, want $want. Output:"$'\n'"$out"
	elif [[ -n "$needle" && "$out" != *"$needle"* ]]; then
		fail "$name: output lacks \"$needle\". Output:"$'\n'"$out"
	else
		pass
	fi
}

sha() { if command -v sha256sum >/dev/null; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi; }

# ── ensure-tools ─────────────────────────────────────────────────────────────
case "$(uname -s)/$(uname -m)" in
Linux/x86_64 | Linux/amd64) plat=linux_amd64 glarch=x64 ;;
Linux/aarch64 | Linux/arm64) plat=linux_arm64 glarch=arm64 ;;
Darwin/x86_64) plat=darwin_amd64 glarch=x64 ;;
Darwin/arm64) plat=darwin_arm64 glarch=arm64 ;;
*) echo "tools-test: skipping ensure-tools cases on $(uname -s)/$(uname -m)"; plat="" ;;
esac

if [[ -n "$plat" ]]; then
	os=${plat%_*}
	# A fake gitleaks 9.9.9 release: an archive whose one member prints its version.
	mkdir -p "$scratch/pkg" "$scratch/mirror/gitleaks/gitleaks/releases/download/v9.9.9"
	printf '#!/bin/sh\necho fake-gitleaks-9.9.9\n' >"$scratch/pkg/gitleaks"
	chmod +x "$scratch/pkg/gitleaks"
	archive="$scratch/mirror/gitleaks/gitleaks/releases/download/v9.9.9/gitleaks_9.9.9_${os}_${glarch}.tar.gz"
	tar -czf "$archive" -C "$scratch/pkg" gitleaks
	good=$(sha "$archive")

	versions() { # versions <file> <version> <sha>
		printf 'GITLEAKS_VERSION=%s\nGITLEAKS_SHA256_%s=%s\n' "$2" "$plat" "$3" >"$1"
	}
	run() { # run <tools dir> <versions file> <base url> [tool…]
		local dir=$1 vf=$2 base=$3
		shift 3
		TOOLS_DIR="$dir" VERSIONS_FILE="$vf" TOOLS_BASE_URL="$base" bash "$ROOT/scripts/ensure-tools.sh" "$@"
	}
	mirror="file://$scratch/mirror"

	# A tampered archive (one checksum digit off) is refused, and nothing is installed.
	versions "$scratch/bad.env" 9.9.9 "${good%?}0"
	[[ "${good: -1}" == 0 ]] && versions "$scratch/bad.env" 9.9.9 "${good%?}1"
	expect 1 "checksum mismatch" "a tampered archive is refused" -- run "$scratch/t1" "$scratch/bad.env" "$mirror" gitleaks
	if [[ -e "$scratch/t1/bin/gitleaks" ]]; then fail "a tampered archive left a binary behind"; else pass; fi

	# The pinned archive installs, and the binary runs.
	versions "$scratch/good.env" 9.9.9 "$good"
	expect 0 "" "the pinned archive installs" -- run "$scratch/t2" "$scratch/good.env" "$mirror" gitleaks
	expect 0 "fake-gitleaks-9.9.9" "the installed binary runs" -- "$scratch/t2/bin/gitleaks"

	# Installed and current: nothing is downloaded (the mirror is gone and it passes).
	expect 0 "" "a current tool needs no network" -- run "$scratch/t2" "$scratch/good.env" "file://$scratch/nowhere" gitleaks

	# A bumped pin that cannot be fetched fails, and the old binary stays whole.
	versions "$scratch/bumped.env" 9.9.10 "$good"
	expect 1 "could not download" "an unreachable bump fails" -- run "$scratch/t2" "$scratch/bumped.env" "$mirror" gitleaks
	expect 0 "fake-gitleaks-9.9.9" "a failed bump keeps the old binary" -- "$scratch/t2/bin/gitleaks"

	# A pin with no checksum for this platform is refused before any download.
	printf 'GITLEAKS_VERSION=9.9.9\n' >"$scratch/nosum.env"
	expect 1 "no gitleaks checksum" "a pin without a checksum is refused" -- run "$scratch/t3" "$scratch/nosum.env" "$mirror" gitleaks

	expect 2 "unknown tool" "an unknown tool is refused" -- run "$scratch/t4" "$scratch/good.env" "$mirror" gitlaeks
fi

# ── ci-parity-check ──────────────────────────────────────────────────────────
parity() { MAKEFILE="$1" CI_YML="$2" bash "$ROOT/scripts/ci-parity-check.sh"; }
mk="$scratch/Makefile" ci="$scratch/ci.yml"
cat >"$mk" <<'EOF'
check: lint test \
	docs-check
EOF
cat >"$ci" <<'EOF'
jobs:
  a:
    steps:
      - name: make lint and friends   # a name is not a command
        run: make lint test COVERPROFILE=x.out
      - run: |
          echo "starting"
          make docs-check
EOF
expect 0 "(3)" "matching lists pass" -- parity "$mk" "$ci"

printf 'check: lint test docs-check vuln\n' >"$scratch/Makefile.more"
expect 1 "runs gates CI does not: vuln" "a gate CI lacks fails" -- parity "$scratch/Makefile.more" "$ci"

sed 's/make docs-check/make docs-check extra/' "$ci" >"$scratch/ci.more"
expect 1 "does not: extra" "a CI target check lacks fails" -- parity "$mk" "$scratch/ci.more"

# `make docs-check` only in a comment or a step name is not CI running it.
cat >"$scratch/ci.comment" <<'EOF'
jobs:
  a:
    steps:
      - name: make docs-check
        run: make lint test # make docs-check
EOF
expect 1 "runs gates CI does not: docs-check" "a target named only in a comment does not count" -- parity "$mk" "$scratch/ci.comment"

cat >"$scratch/ci.direct" <<'EOF'
jobs:
  a:
    steps:
      - run: make lint test docs-check
      - run: |
          set -e
          go test -race ./...
EOF
expect 1 "calls a tool directly" "a tool called around its target fails" -- parity "$mk" "$scratch/ci.direct"

printf 'all: build\n' >"$scratch/Makefile.none"
expect 1 "no check: rule" "a Makefile without check fails" -- parity "$scratch/Makefile.none" "$ci"

# And the real files agree.
expect 0 "✓" "the project's Makefile and ci.yml agree" -- parity "$ROOT/Makefile" "$ROOT/.github/workflows/ci.yml"

echo "tools-test: $((ran - failures))/$ran passed"
((failures == 0))
