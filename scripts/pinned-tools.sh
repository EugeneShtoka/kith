#!/usr/bin/env bash
# pinned-tools: the tools pinned outside go.mod (Dependabot bumps actions and Go
# modules, not these), each against its latest release, and the bubbletea fork's base
# against upstream. The tool pins live in one place, tools/versions.env and
# tools/markdownlint's lock, which CI, the release and `make check` all read. Exits 1
# when a pin is behind, when a pinned binary lacks a checksum for a platform
# ensure-tools installs on, or when the lock and its package.json disagree.
#
#   scripts/pinned-tools.sh            # needs network; uses $GH_TOKEN when set
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
# shellcheck source=../tools/versions.env
. tools/versions.env

latest() {
	local api="https://api.github.com/repos/$1/releases/latest"
	local -a auth=()
	if [[ -n "${GH_TOKEN:-}" ]]; then auth=(-H "Authorization: Bearer $GH_TOKEN"); fi
	# ${auth[@]+…}: an empty array under `set -u` fails on bash 3.2 (macOS).
	curl -fsSL ${auth[@]+"${auth[@]}"} "$api" | sed -nE 's/^ *"tag_name": *"v?([^"]+)".*/\1/p' | head -1
}

status=0
row() { printf '%-18s %-10s %-10s %s\n' "$@"; }
row tool pinned latest ""

# name | repository | versions.env prefix | has per-platform checksums
for entry in \
	"goreleaser|goreleaser/goreleaser|GORELEASER|yes" \
	"gitleaks|gitleaks/gitleaks|GITLEAKS|yes" \
	"buf|bufbuild/buf|BUF|yes" \
	"syft|anchore/syft|SYFT|no" \
	"cosign|sigstore/cosign|COSIGN|no"; do
	IFS='|' read -r name repo var sums <<<"$entry"
	version_var="${var}_VERSION"
	pinned="${!version_var:-}"
	if [[ -z "$pinned" ]]; then
		echo "$name: no $version_var in tools/versions.env" >&2
		status=1
		continue
	fi
	if [[ "$sums" == yes ]]; then
		for plat in linux_amd64 linux_arm64 darwin_amd64 darwin_arm64; do
			sum_var="${var}_SHA256_$plat"
			if [[ ! "${!sum_var:-}" =~ ^[0-9a-f]{64}$ ]]; then
				echo "$name: $sum_var is missing or not a SHA-256" >&2
				status=1
			fi
		done
	fi
	newest=$(latest "$repo" || true)
	note=""
	if [[ -z "$newest" ]]; then
		note="(could not ask GitHub)"
		status=1
	elif [[ "$newest" != "$pinned" ]]; then
		note="← behind"
		status=1
	fi
	row "$name" "$pinned" "${newest:-?}" "$note"
done

# The bubbletea fork (go.mod's replace, rebased by hand; Dependabot ignores it): its
# base release against upstream's newest, so a rebase is due when upstream moves.
fork=$(sed -nE 's#^replace charm.land/bubbletea/v2 => [^ ]+ (v[0-9.]+)-[a-z0-9.]+$#\1#p' go.mod)
if [[ -z "$fork" ]]; then
	echo "bubbletea fork: no replace line of the form <module> vX.Y.Z-<name> in go.mod" >&2
	status=1
else
	newest=$(latest charmbracelet/bubbletea || true)
	note=""
	if [[ -z "$newest" ]]; then
		note="(could not ask GitHub)"
		status=1
	elif [[ "v$newest" != "$fork" ]]; then
		note="← rebase the fork"
		status=1
	fi
	row "bubbletea (fork)" "${fork#v}" "${newest:-?}" "$note"
fi

# markdownlint-cli2: the exact version package.json asks for is the one locked.
wanted=$(sed -nE 's/^ *"markdownlint-cli2": *"([^"]+)".*/\1/p' tools/markdownlint/package.json | head -1)
locked=$(awk '/"node_modules\/markdownlint-cli2"/ { getline; print; exit }' tools/markdownlint/package-lock.json |
	sed -nE 's/^ *"version": *"([^"]+)".*/\1/p')
if [[ -z "$wanted" || "$wanted" != "$locked" ]]; then
	echo "markdownlint-cli2: package.json asks for '${wanted:-?}', the lock holds '${locked:-?}' (npm install --package-lock-only)" >&2
	status=1
fi
newest=$(npm view markdownlint-cli2 version 2>/dev/null || true)
note=""
if [[ -z "$newest" ]]; then
	note="(could not ask npm)"
	status=1
elif [[ "$newest" != "$locked" ]]; then
	note="← behind"
	status=1
fi
row markdownlint-cli2 "${locked:-?}" "${newest:-?}" "$note"
exit "$status"
