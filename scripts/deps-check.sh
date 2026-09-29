#!/usr/bin/env bash
#
# deps-check.sh — what each client links, transitively. depguard sees one file's
# direct imports; this sees the whole graph, so a package two hops away cannot carry
# the cache, the Matrix SDK or a model server into a binary that must not have them.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

mod=github.com/EugeneShtoka/kith
# The single-writer cache, the crypto store's driver and the Matrix SDK.
daemon_only="^($mod/internal/(db|matrix|session|modelsetup)|maunium\.net/go/mautrix|modernc\.org/sqlite)(/|$)"
# The model clients and the process that serves a model.
models="^$mod/internal/(llamacpp|llm)$"
# What draws a terminal UI: Charm's libraries, and the packages built on them.
drawing="^(charm\.land/|github\.com/charmbracelet/|$mod/internal/(tui|theme)$)"

status=0
check() {
	local pkg=$1 pattern=$2 why=$3 found
	found="$(GOFLAGS=-tags=goolm go list -deps "$pkg" | grep -E "$pattern" || true)"
	if [[ -n $found ]]; then
		echo "deps-check: $pkg links what it must not ($why):" >&2
		sed 's/^/  /' <<<"$found" >&2
		status=1
	fi
}

check ./cmd/kith-mcp "$daemon_only" "it talks to the daemon, never to the cache or the homeserver"
check ./internal/tui "$daemon_only" "the TUI reaches everything through api.Backend"
check ./internal/daemon "$daemon_only" "clients link the wire"
check ./internal/tui "$models" "model work runs in the daemon"
check ./cmd/kith-mcp "$models" "model work runs in the daemon"
check ./cmd/kithd "$drawing" "the daemon is headless: it draws nothing"
check ./cmd/kith-mcp "$drawing" "kith-mcp is headless: it draws nothing"

(( status == 0 )) && echo "deps-check: OK"
exit $status
