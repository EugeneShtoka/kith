#!/usr/bin/env bash
#
# coverage-gate.sh — enforce coverage floors: hand-written total >= TOTAL_MIN and
# every non-exempt package >= PKG_MIN (a package with no tests counts as 0%).
#
# Usage: bash scripts/coverage-gate.sh [coverage.out]   (or `make coverage-check`)

set -euo pipefail

MODULE="github.com/EugeneShtoka/kith"
PROFILE="${1:-coverage.out}"
# A little under the current figures: catch a slide, not noise.
TOTAL_MIN="${TOTAL_MIN:-76}"
PKG_MIN="${PKG_MIN:-55}"

# Generated code is excluded from the gated total.
GENERATED_RE='\.pb\.go|\.connect\.go|emoji_generated\.go'

# Exempt only what *cannot* be unit-tested, with a reason. Stale entries fail.
exempt=(
	internal/api                              # interface declarations, no logic
	internal/api/backend/v1                   # generated protobuf types
	internal/api/backend/v1/backendv1connect  # generated Connect stubs
	internal/apitest                          # test-only fakes (Nop backend)
	internal/filedialog                       # XDG desktop portal over D-Bus; needs a session bus and a human to pick a file
	cmd/kith                                # main wiring
	cmd/kithd                               # main wiring (daemon side); serve()'s workers are exercised through internal/daemon
	cmd/emoji-probe                           # interactive terminal diagnostic: writes glyphs to a tty and asks what you see
)

is_exempt() {
	local p="$1" e
	for e in "${exempt[@]}"; do [[ "$p" == "$e" ]] && return 0; done
	return 1
}

[[ -f "$PROFILE" ]] || { echo "coverage-gate: profile not found: $PROFILE" >&2; exit 2; }

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
stale=0
for e in "${exempt[@]}"; do
	[[ -d "$ROOT/$e" ]] || { echo "coverage-gate: exempt entry names no such package: $e" >&2; stale=1; }
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
trap 'rm -f "$handwritten"' EXIT
head -1 "$PROFILE" > "$handwritten"
{ grep -vE "$GENERATED_RE" "$PROFILE" || true; } | tail -n +2 >> "$handwritten"

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
