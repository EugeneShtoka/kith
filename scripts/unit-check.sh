#!/usr/bin/env bash
#
# unit-check.sh — kithd.service must mkdir every ReadWritePaths= entry outside %t in
# its ExecStartPre=+mkdir line: the sandbox is built before the daemon could make them.

set -euo pipefail

unit=packaging/systemd/kithd.service

if [[ ! -f $unit ]]; then
	echo "unit-check: $unit is missing" >&2
	exit 1
fi

rw=$(sed -n 's/^ReadWritePaths=//p' "$unit" | tr ' ' '\n' | sed 's/^-//' | grep -v '^%t' | grep -v '^$' | sort)
mk=$(sed -n 's/^ExecStartPre=+mkdir -p //p' "$unit" | tr ' ' '\n' | grep -v '^$' | sort)
if [[ $rw != "$mk" ]]; then
	cat >&2 <<MSG
unit-check: FAIL — $unit creates different directories than it opens for writing.

ReadWritePaths= (minus %t):
$rw

ExecStartPre=+mkdir -p:
$mk

Every ReadWritePaths= entry outside %t must be in the mkdir line, and vice versa.
MSG
	exit 1
fi
echo "unit-check: OK — $unit creates every directory it opens for writing"
