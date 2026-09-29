#!/usr/bin/env bash
#
# actions-pin-check.sh — every `uses:` in workflows and composite actions must be
# pinned to a 40-char commit SHA with a `# vX.Y.Z` comment (docker refs: @sha256:).
# Mutable tags can be retagged by an attacker (cf. CVE-2025-30066).
# Local actions (`uses: ./...`) are skipped; their action.yml is scanned instead.

set -euo pipefail

shopt -s nullglob globstar
# Workflows plus every composite action.yml in the tree (vendor excluded).
workflows=()
while IFS= read -r f; do
	[[ -n "$f" ]] && workflows+=("$f")
done < <(
	{ printf '%s\n' .github/workflows/*.yml .github/workflows/*.yaml
	  printf '%s\n' **/action.yml **/action.yaml
	} 2>/dev/null | grep -vE '^(vendor|node_modules)/' | sort -u
)
if [[ ${#workflows[@]} -eq 0 ]]; then
	echo "actions-pin-check: no workflow files found under .github/workflows/" >&2
	exit 1
fi

fail=0
for wf in "${workflows[@]}"; do
	# Match `uses:` anywhere on the line (YAML flow mappings too).
	while IFS= read -r line; do
		spec="${line#*uses:}"
		spec="${spec%%#*}"
		spec="$(echo "$spec" | tr -d '"'"'"' ' | tr -d '[:space:]')"

		[[ -z "$spec" ]] && continue
		[[ "$spec" == ./* ]] && continue # local composite action; its own file is scanned above

		if [[ "$spec" == docker://* ]]; then
			if [[ "$spec" != *@sha256:* ]]; then
				echo "❌ $wf: docker ref must be pinned to an @sha256: digest: $spec" >&2
				fail=1
			fi
			continue
		fi

		if [[ "$spec" != *@* ]]; then
			echo "❌ $wf: unpinned (no ref): $spec" >&2
			fail=1
			continue
		fi

		ref="${spec##*@}"
		if [[ ! "$ref" =~ ^[0-9a-f]{40}$ ]]; then
			echo "❌ $wf: action must be pinned to a 40-char commit SHA, got '@$ref': $spec" >&2
			fail=1
			continue
		fi

		# The version comment lets reviewers and Dependabot read the pin.
		if [[ ! "$line" =~ \#[[:space:]]*v[0-9] ]]; then
			echo "❌ $wf: SHA pin carries no '# vX.Y.Z' comment: $spec" >&2
			fail=1
		fi
	done < <(grep -E '(^|[[:space:],{])uses:' "$wf" || true)
done

if [[ "$fail" -ne 0 ]]; then
	echo "" >&2
	echo "GitHub Actions must be pinned to full commit SHAs (with a '# vX.Y.Z' comment)." >&2
	exit 1
fi

echo "actions-pin-check: all workflow actions are SHA-pinned ✓"
