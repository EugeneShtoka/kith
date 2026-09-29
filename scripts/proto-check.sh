#!/usr/bin/env bash
# Generated wire types must match api/proto. The code is generated into a scratch
# directory and compared with the tree, so the check needs no git: it works in a
# checkout with nothing committed, and a newly generated file (untracked) fails it
# just as a changed one does.
#
# Without buf it prints SKIPPED, unless PROTO_CHECK_REQUIRED=1 (CI), where a missing
# buf is a broken gate rather than a skipped one.
set -euo pipefail

if ! command -v buf >/dev/null 2>&1; then
	if [ "${PROTO_CHECK_REQUIRED:-}" = 1 ]; then
		echo "proto-check: buf is not installed, and this gate is required here" >&2
		exit 1
	fi
	echo "proto-check: SKIPPED — buf is not installed (https://buf.build/docs/installation)."
	echo "             CI runs this gate; edit a .proto without buf and the pipeline is where you find out."
	exit 0
fi

buf lint

scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
buf generate --output "$scratch"

# File by file: hand-written code (protoconv) lives beside the generated files.
stale=0
while IFS= read -r generated; do
	rel=${generated#"$scratch"/}
	if ! cmp -s "$generated" "$rel"; then
		echo "proto-check: $rel is missing or differs from what api/proto generates" >&2
		stale=1
	fi
done < <(find "$scratch" -type f)
# And nothing generated-looking is left over that the schema no longer produces.
while IFS= read -r kept; do
	if [[ ! -f $scratch/$kept ]]; then
		echo "proto-check: $kept is no longer generated from api/proto; delete it" >&2
		stale=1
	fi
done < <(find internal/api -type f \( -name '*.pb.go' -o -name '*.connect.go' \))
if [[ $stale -ne 0 ]]; then
	echo "proto-check: run 'make proto' and commit the result" >&2
	exit 1
fi
echo "proto-check: schema lints and the generated code is in sync ✓"
