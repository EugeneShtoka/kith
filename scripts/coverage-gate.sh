#!/usr/bin/env bash
#
# coverage-gate.sh — enforce coverage floors: hand-written total >= TOTAL_MIN and
# every non-exempt package >= PKG_MIN (a package with no tests counts as 0%).
#
# Usage: bash scripts/coverage-gate.sh [coverage.out ...]   (or `make coverage-check`)
# Several profiles (CI's test jobs, one per package group) are merged first.

set -euo pipefail

MODULE="github.com/EugeneShtoka/kith"
PROFILE="${1:-coverage.out}"
# A little under the current figures: catch a slide, not noise.
TOTAL_MIN="${TOTAL_MIN:-76}"
PKG_MIN="${PKG_MIN:-55}"

# Generated code is what Go's convention marks so: a "// Code generated ... DO NOT EDIT."
# line before the package clause. It is left out of the gated total, and a package of
# nothing else (a wire's protobuf types and Connect stubs) is exempt without a list
# entry; one hand-written file in it ends the exemption.
GENERATED_HEADER='^// Code generated .* DO NOT EDIT\.$'

# Exempt only what *cannot* be unit-tested, with a reason. Stale entries fail.
exempt=(
	internal/api                              # interface declarations, no logic
	internal/apitest                          # test-only fakes (Nop backend)
	internal/filedialog                       # XDG desktop portal over D-Bus; needs a session bus and a human to pick a file
	cmd/kith                                # main wiring
	cmd/kithd                               # main wiring (daemon side); serve()'s workers are exercised through internal/daemon
	cmd/kith-signal                         # main wiring (Signal helper); its service and heartbeat are exercised through internal/signalhelper
	cmd/emoji-probe                           # interactive terminal diagnostic: writes glyphs to a tty and asks what you see
)

# generated reports whether the Go file at path is generated (GENERATED_HEADER before
# its package clause).
generated() {
	awk -v re="$GENERATED_HEADER" 'BEGIN { r = 1 } /^package / { exit } $0 ~ re { r = 0; exit } END { exit r }' "$1"
}

is_exempt() {
	local p="$1" e
	for e in "${exempt[@]}"; do [[ "$p" == "$e" ]] && return 0; done
	return 1
}

for f in "${@:-$PROFILE}"; do
	[[ -f "$f" ]] || { echo "coverage-gate: profile not found: $f" >&2; exit 2; }
done
if (( $# > 1 )); then
	# One mode line, then every profile's statements (the groups' packages are disjoint).
	merged="$(mktemp)"
	{ head -1 "$1"; for f in "$@"; do tail -n +2 "$f"; done; } > "$merged"
	PROFILE="$merged"
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# Every generated file, module-relative, and every package of generated files only.
declare -A generated_file generated_pkg
while IFS=$'\t' read -r fq dir files; do
	pkg="${fq#"$MODULE"/}"
	own=0 gen=0
	for f in $files; do
		if generated "$dir/$f"; then generated_file["$pkg/$f"]=1; gen=1; else own=1; fi
	done
	(( gen && !own )) && generated_pkg["$pkg"]=1
done < <(go list -f '{{.ImportPath}}{{"\t"}}{{.Dir}}{{"\t"}}{{join .GoFiles " "}}' ./...)

stale=0
for e in "${exempt[@]}"; do
	[[ -d "$ROOT/$e" ]] || { echo "coverage-gate: exempt entry names no such package: $e" >&2; stale=1; }
	[[ -n "${generated_pkg["$e"]:-}" ]] && { echo "coverage-gate: exempt entry is generated, so exempt already: $e" >&2; stale=1; }
done
(( stale )) && { echo "coverage-gate: fix the exempt list in $0" >&2; exit 2; }

# Aggregate per-package covered/total statements straight from the profile.
# Lines: <import/path/file.go>:<s>.<c>,<e>.<c> <numstmts> <count>
declare -A total covered
while IFS= read -r line; do
	[[ "$line" == mode:* ]] && continue
	path="${line%%:*}"
	rest="${line#*:}"
	pkg="${path%/*}"          # drop filename → package import path
	pkg="${pkg#"$MODULE"/}"   # make module-relative
	read -r _pos stmts cnt <<<"$rest"
	total["$pkg"]=$(( ${total["$pkg"]:-0} + stmts ))
	(( cnt > 0 )) && covered["$pkg"]=$(( ${covered["$pkg"]:-0} + stmts ))
done < "$PROFILE"

fail=0
printf '%-48s %8s\n' "PACKAGE" "COVER"
printf '%-48s %8s\n' "-------" "-----"
while IFS= read -r fq; do
	pkg="${fq#"$MODULE"/}"
	if [[ -n "${generated_pkg["$pkg"]:-}" ]]; then
		printf '%-48s %8s\n' "$pkg" "generated"
		continue
	fi
	if is_exempt "$pkg"; then
		printf '%-48s %8s\n' "$pkg" "exempt"
		continue
	fi
	t=${total["$pkg"]:-0}
	if (( t == 0 )); then
		printf '%-48s %8s  FAIL (no tests)\n' "$pkg" "0.0%"
		fail=1
		continue
	fi
	c=${covered["$pkg"]:-0}
	pct=$(awk "BEGIN{printf \"%.1f\", 100*$c/$t}")
	if awk "BEGIN{exit !($pct < $PKG_MIN)}"; then
		printf '%-48s %7s%%  FAIL (< %s%%)\n' "$pkg" "$pct" "$PKG_MIN"
		fail=1
	else
		printf '%-48s %7s%%\n' "$pkg" "$pct"
	fi
done < <(go list ./...)

# Gate on hand-written code; print the raw total (what `make coverage` shows) too.
handwritten="$(mktemp)"
trap 'rm -f "$handwritten" "${merged:-}"' EXIT
head -1 "$PROFILE" > "$handwritten"
tail -n +2 "$PROFILE" | while IFS= read -r line; do
	file="${line%%:*}"
	[[ -n "${generated_file["${file#"$MODULE"/}"]:-}" ]] || printf '%s\n' "$line"
done >> "$handwritten"

tot=$(go tool cover -func="$handwritten" | awk '/^total:/{sub(/%/,"",$NF); print $NF}')
raw=$(go tool cover -func="$PROFILE" | awk '/^total:/{sub(/%/,"",$NF); print $NF}')
echo
echo "overall: ${tot}% hand-written (min ${TOTAL_MIN}%), ${raw}% including generated code"
echo "per-package floor: ${PKG_MIN}%"
# In CI, the same line on the run's summary page.
if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
	printf '### Test coverage\n\n%s%% hand-written (floor %s%%), %s%% including generated code\n' \
		"$tot" "$TOTAL_MIN" "$raw" >>"$GITHUB_STEP_SUMMARY"
fi
if awk "BEGIN{exit !($tot < $TOTAL_MIN)}"; then
	echo "FAIL: hand-written coverage ${tot}% is below ${TOTAL_MIN}%"
	fail=1
fi

if (( fail )); then
	echo "coverage-gate: FAILED" >&2
	exit 1
fi
echo "coverage-gate: PASSED"
