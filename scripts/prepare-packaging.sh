#!/usr/bin/env bash
#
# prepare-packaging.sh — GoReleaser `before` hook: renders the units with
# ExecStart=/usr/bin/kithd and writes config.toml.example (`kith
# --print-config`) into build/packaging/ (or OUTDIR).
#
# Usage: bash scripts/prepare-packaging.sh [OUTDIR]

set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
out=${1:-$here/build/packaging}

rm -rf "$out"
mkdir -p "$out"
bash "$here/scripts/render-units.sh" /usr/bin "$out/systemd"

# Host build; goolm passed explicitly so GOFLAGS need not be set.
(cd "$here" && CGO_ENABLED=0 go run -tags=goolm ./cmd/kith --print-config) >"$out/config.toml.example.tmp"
[[ -s "$out/config.toml.example.tmp" ]] || {
	echo "prepare-packaging: kith --print-config printed nothing" >&2
	exit 1
}
chmod 0644 "$out/config.toml.example.tmp"
mv "$out/config.toml.example.tmp" "$out/config.toml.example"
echo "prepare-packaging: wrote $out"
