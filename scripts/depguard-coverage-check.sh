#!/usr/bin/env bash
#
# depguard-coverage-check.sh — every package under internal/ and cmd/ must be
# named by some depguard rule's `files:` pattern in .golangci.yml; an unnamed
# package has no import boundary and lints clean. Checks existence, not correctness.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

config=.golangci.yml
[[ -f $config ]] || { echo "depguard-coverage-check: $config is missing" >&2; exit 2; }

# The paths rules name, in two kinds: `**/internal/spell/**` guards that directory and
# everything below it; `**/internal/api/*.go` guards that one directory only.
patterns="$(grep -oE '"\*\*/(internal|cmd)/[a-zA-Z0-9_./*-]+"' "$config" | tr -d '"' | sed 's/^\*\*\///')"
subtree="$(sed -n 's:/\*\*$::p' <<<"$patterns" | sort -u)"
dironly="$(sed -n 's:/\*\.go$::p' <<<"$patterns" | sort -u)"

missing=()
# Every directory holding non-test Go files is a package, at any depth.
while IFS= read -r dir; do
	covered=no
	if grep -qxF "$dir" <<<"$dironly"; then covered=yes; fi
	probe=$dir
	while [[ $covered == no ]]; do
		if grep -qxF "$probe" <<<"$subtree"; then covered=yes; break; fi
		[[ $probe == */* ]] || break
		probe="${probe%/*}"
	done
	[[ $covered == yes ]] || missing+=("$dir")
done < <(find internal cmd -name '*.go' ! -name '*_test.go' -printf '%h\n' | sort -u)

if (( ${#missing[@]} )); then
	echo "depguard-coverage-check: no depguard rule names these packages:" >&2
	printf '  %s\n' "${missing[@]}" >&2
	cat >&2 <<'EOF'

Each one may currently import anything — the cache, the Matrix SDK, bubbletea — and
`golangci-lint run` will report 0 issues about it.

Add a rule to .golangci.yml under linters.settings.depguard.rules. A leaf package wants an
allow list, which is the shape that cannot fall behind:

    <name>-is-a-leaf:
      files:
        - "**/<path>/**"
      allow:
        - "$gostd"
        - "github.com/EugeneShtoka/kith/<path>"
EOF
	exit 1
fi

echo "depguard-coverage-check: every package under internal/ and cmd/ is named by a rule ✓"
