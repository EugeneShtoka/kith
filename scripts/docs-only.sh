#!/usr/bin/env bash
#
# docs-only.sh — succeeds when every path on stdin (one per line, as `git diff
# --name-only` prints them) is documentation: a Markdown file outside a testdata
# directory, or anything under docs/. An empty list is not docs-only. The pre-push hook
# and CI both ask it, so a change of docs alone runs the docs gate (docs-check: the
# Markdown lint and the tests that read docs/) and not the rest.
#
#   git diff --name-only origin/main...HEAD | bash scripts/docs-only.sh
set -euo pipefail

seen=0
while IFS= read -r path; do
	[[ -z "$path" ]] && continue
	seen=1
	case "$path" in
	*/testdata/* | testdata/*) exit 1 ;;
	docs/* | *.md) ;;
	*) exit 1 ;;
	esac
done
((seen))
