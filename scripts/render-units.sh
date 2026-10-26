#!/usr/bin/env bash
#
# render-units.sh — write the systemd user units with ExecStart= pointing at BINDIR.
# The single place this rewrite lives (Makefile, GoReleaser, PKGBUILD, flake).
#
# Usage: bash scripts/render-units.sh BINDIR OUTDIR

set -euo pipefail

if [[ $# -ne 2 ]]; then
	echo "usage: $0 BINDIR OUTDIR" >&2
	exit 2
fi
bindir=${1%/}
outdir=$2

here=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
src="$here/packaging/systemd"

mkdir -p "$outdir"
for unit in kithd.service kith-notify.service; do
	bin=${unit%.service}
	sed "s|^ExecStart=[^ ]*/$bin|ExecStart=$bindir/$bin|" "$src/$unit" >"$outdir/$unit.tmp"
	if ! grep -q "^ExecStart=$bindir/$bin\( \|$\)" "$outdir/$unit.tmp"; then
		rm -f "$outdir/$unit.tmp"
		echo "render-units: $unit has no ExecStart= ending in /$bin to rewrite" >&2
		exit 1
	fi
	chmod 0644 "$outdir/$unit.tmp"
	mv "$outdir/$unit.tmp" "$outdir/$unit"
done
