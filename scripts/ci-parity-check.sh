#!/usr/bin/env bash
# ci-parity-check: `make check` and CI run the same gates. Every prerequisite of
# `check` must be a `make` target some CI step runs, and every make target CI runs
# must be a `check` gate. CI steps may not call the tools directly either (go
# build/test/vet, go tool, gitleaks, buf, goreleaser, markdownlint): a gate spelled
# twice drifts, as the hand-written CI steps and the Makefile did before this check.
#
#   scripts/ci-parity-check.sh   (MAKEFILE and CI_YML override the files, for the tests)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MAKEFILE="${MAKEFILE:-$ROOT/Makefile}"
CI_YML="${CI_YML:-$ROOT/.github/workflows/ci.yml}"

# The `check:` rule, continuation lines joined.
gates=$(awk '
	/^check:/ { on = 1; sub(/^check:/, "") }
	on { line = $0; cont = sub(/\\$/, "", line); printf "%s ", line; if (!cont) exit }
' "$MAKEFILE" | tr -s ' \t' '\n' | sed '/^$/d' | sort -u)
if [[ -z "$gates" ]]; then
	echo "ci-parity-check: no check: rule in $MAKEFILE" >&2
	exit 1
fi

# What CI steps run: each `run:` value, and every line of a `run: |` block.
commands=$(awk '
	function indent(s) { match(s, /^ */); return RLENGTH }
	block && (indent($0) > base || $0 ~ /^[[:space:]]*$/) { print; next }
	{ block = 0 }
	/^[[:space:]]*(- )?run:/ {
		v = $0; sub(/^[[:space:]]*(- )?run:[[:space:]]*/, "", v)
		if (v ~ /^[|>]/) { block = 1; base = indent($0) } else print v
	}
' "$CI_YML" | sed -E 's/(^|[[:space:]])#.*$//')

# The targets of every `make …` in them (VAR=value arguments and flags dropped).
ci=$(echo "$commands" |
	grep -oE '(^|[;&|[:space:]])make( +[^;&|]*)?' |
	sed -E 's/^[;&|[:space:]]*make//' | tr -s ' \t' '\n' |
	grep -vE '^$|=|^-' | sort -u || true)

status=0
missing=$(comm -23 <(echo "$gates") <(echo "$ci"))
extra=$(comm -13 <(echo "$gates") <(echo "$ci"))
if [[ -n "$missing" ]]; then
	echo "ci-parity-check: \`make check\` runs gates CI does not:" $missing >&2
	status=1
fi
if [[ -n "$extra" ]]; then
	echo "ci-parity-check: CI runs make targets \`make check\` does not:" $extra >&2
	status=1
fi

direct=$(echo "$commands" |
	grep -E '(^|[^[:alnum:]_./-])(go (build|test|vet|tool)|gitleaks|buf|goreleaser|markdownlint-cli2|govulncheck|golangci-lint)( |$)' || true)
if [[ -n "$direct" ]]; then
	echo "ci-parity-check: CI calls a tool directly; run it through its make target:" >&2
	echo "$direct" >&2
	status=1
fi

if ((status == 0)); then
	echo "ci-parity-check: CI runs exactly the \`make check\` gates ($(echo "$gates" | wc -l | tr -d ' ')) ✓"
fi
exit "$status"
