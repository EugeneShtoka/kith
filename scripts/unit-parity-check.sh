#!/usr/bin/env bash
#
# unit-parity-check.sh — kithd.service and kithd@.service must agree on every
# directive except the allow-listed ones, and each must mkdir every ReadWritePaths=
# entry outside %t in its ExecStartPre=+mkdir line.

set -euo pipefail

plain=packaging/systemd/kithd.service
templated=packaging/systemd/kithd@.service

for f in "$plain" "$templated"; do
	if [[ ! -f $f ]]; then
		echo "unit-parity-check: $f is missing" >&2
		exit 1
	fi
done

# The template names the profile and passes --profile %i.
allowed='^(Description|ExecStart)='

# Directives only; section headers kept so a moved directive still differs.
directives() {
	sed -e 's/[[:space:]]*$//' "$1" |
		grep -Ev '^[[:space:]]*(#|$)' |
		grep -Ev "$allowed"
}

for f in "$plain" "$templated"; do
	rw=$(sed -n 's/^ReadWritePaths=//p' "$f" | tr ' ' '\n' | sed 's/^-//' | grep -v '^%t' | grep -v '^$' | sort)
	mk=$(sed -n 's/^ExecStartPre=+mkdir -p //p' "$f" | tr ' ' '\n' | grep -v '^$' | sort)
	if [[ $rw != "$mk" ]]; then
		cat >&2 <<EOF
unit-parity-check: FAIL — $f creates different directories than it opens for writing.

ReadWritePaths= (minus %t):
$rw

ExecStartPre=+mkdir -p:
$mk

Every ReadWritePaths= entry outside %t must be in the mkdir line, and vice versa.
EOF
		exit 1
	fi
done

if diff_out=$(diff <(directives "$plain") <(directives "$templated")); then
	echo "unit-parity-check: OK — the two units agree on every shared directive, and each creates every directory it opens for writing"
	exit 0
fi

cat >&2 <<EOF
unit-parity-check: FAIL — $plain and $templated disagree.

Both files configure the same daemon under the same sandbox, so a directive in one
belongs in the other. Add it to both, or — if the difference is deliberate — add the
directive to the allow-list at the top of this script with the reason it differs.

  < $plain
  > $templated

$diff_out
EOF
exit 1
