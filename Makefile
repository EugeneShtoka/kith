.PHONY: build build-tui build-daemon build-mcp run run-daemon install deploy undeploy \
	daemon-status daemon-logs daemon-restart daemon-stop \
	test lint fmt fmt-check fix vuln secrets pin-check unit-check arch-check mod-check release-check \
	proto-check docs-check nix-check hooks check coverage coverage-check tools pins-outdated \
	compile cross ci-parity ensure-tools vet script-check quick test-quick arch-quick \
	proto emoji dict-manifest freq-manifest model-manifest keys-doc

# goolm = mautrix's pure-Go E2EE (no libolm/cgo). Also set in .golangci.yml,
# .goreleaser.yaml and the CI env.
export GOFLAGS := -tags=goolm

# ── The same environment as CI ───────────────────────────────────────────────
# Every gate runs what CI runs, with what CI runs it with:
#  - Go: exactly go.mod's `toolchain` line (downloaded once), not whatever go is on
#    PATH, which may be newer. GOTOOLCHAIN=local opts out, for offline work.
#  - No user `go env` file: CI has none, and one can carry GOEXPERIMENT or GOFLAGS
#    that change the build.
#  - golangci-lint, govulncheck and the protoc plugins: the go.mod `tool` directive.
#  - goreleaser, gitleaks, buf and markdownlint-cli2: tools/versions.env and
#    tools/markdownlint's lock, installed into .tools by scripts/ensure-tools.sh,
#    which CI runs too.
# scripts/ci-parity-check.sh fails when a `check` gate is not a CI step or back.
GO_TOOLCHAIN := $(shell awk '$$1 == "toolchain" {print $$2}' go.mod)
export GOTOOLCHAIN := $(GO_TOOLCHAIN)
export GOENV := off
export PATH := $(CURDIR)/.tools/bin:$(PATH)
ENSURE   := bash scripts/ensure-tools.sh
GOLANGCI := go tool golangci-lint
GOVULN   := go tool govulncheck

# CHECK_STRICT=1 (the pre-push hook) fails a gate that would otherwise be skipped
# here, which is only the history secret scan outside the project's own repository.
CHECK_STRICT ?=

# ── Build ────────────────────────────────────────────────────────────────────
# Built as released: CGO_ENABLED=0. With cgo on, mautrix's cryptohelper links
# mattn/go-sqlite3 (via dbutil/litestream) although only modernc is opened.
# `test` keeps cgo: -race needs it.
# Outside its own repository (the tree sits in the $$HOME dotfiles repo) go would
# stamp that repository's commit into the binaries, so vcs stamping is off there.
OWN_REPO := $(shell [ "$$(git rev-parse --show-toplevel 2>/dev/null)" = "$(CURDIR)" ] && echo yes)
BUILDVCS := $(if $(OWN_REPO),,-buildvcs=false)
GOBUILD := CGO_ENABLED=0 go build $(BUILDVCS)
GORUN   := CGO_ENABLED=0 go run $(BUILDVCS)
# Every package, as released (CI's Build step).
compile:
	$(GOBUILD) ./...

# The release targets plus windows (built, not released). CI runs the same target.
CROSS_TARGETS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 freebsd/amd64 freebsd/arm64 \
	windows/amd64 windows/arm64
cross:
	@set -e; for t in $(CROSS_TARGETS); do \
		echo "cross: $$t"; GOOS=$${t%/*} GOARCH=$${t#*/} $(GOBUILD) ./...; \
	done

# The client needs the daemon (no in-process fallback), so build both by default.
build: build-tui build-daemon build-mcp

build-tui:
	$(GOBUILD) -o kith ./cmd/kith/

# kith's own Telegram app, from the environment (CI: secrets), never the repository:
# the shell expands them, so make does not echo the hash. Unset, the build has none.
TELEGRAM_X := -X github.com/EugeneShtoka/kith/internal/telegram.builtinID=$${TELEGRAM_API_ID:-} \
	-X github.com/EugeneShtoka/kith/internal/telegram.builtinHash=$${TELEGRAM_API_HASH:-}

build-daemon:
	$(GOBUILD) -ldflags "$(TELEGRAM_X)" -o kithd ./cmd/kithd/

build-mcp:
	$(GOBUILD) -o kith-mcp ./cmd/kith-mcp/

# ── Run from source ──────────────────────────────────────────────────────────
# Spawns a detached daemon if none runs — the *installed* one, if present.
run:
	$(GORUN) ./cmd/kith/

# Foreground daemon. Exits 0 at once if the unit already holds the lock.
run-daemon:
	$(GORUN) ./cmd/kithd/

# One coverage profile per make invocation, so concurrent runs (agents, hooks, a
# developer) never write the same file. Pass COVERPROFILE=path to choose one.
# Named by make's PID: unique per invocation, and nothing is created until go test
# writes it.
ifndef COVERPROFILE
COVERPROFILE := $(or $(TMPDIR),/tmp)/kith-cover-$(shell echo $$PPID).out
endif

# -count=1: never serve cached results. The profile feeds coverage-check.
test:
	go test -race -count=1 -coverprofile=$(COVERPROFILE) ./...

# The pre-push gate's tests: no race detector, and from Go's test cache, so a package
# whose code and tests did not change is not run again. CI runs `test`.
test-quick:
	go test ./...

# --allow-serial-runners: a second concurrent run waits for the first instead of
# failing with "parallel golangci-lint is running".
lint:
	$(GOLANGCI) run --allow-serial-runners ./...

fmt:
	$(GOLANGCI) fmt ./...

# Reports diffs without rewriting (for check/CI).
fmt-check:
	$(GOLANGCI) fmt --diff ./...

# go vet alone (the pre-commit hook; lint runs it too, with more).
vet:
	go vet ./...

# Auto-fix everything that can be fixed without human judgment.
fix:
	go mod tidy
	go fmt ./...
	$(GOLANGCI) run --fix ./...

# Go modules, and the one npm tree the gates run (markdownlint-cli2's lock).
vuln:
	$(GOVULN) ./...
	scripts/npm-audit.sh

# Regenerate api/proto codegen (needs buf). Output is committed.
proto:
	buf lint
	buf generate

# Generated tables/manifests below are committed so builds need no network.
emoji:
	python3 scripts/gen-emoji.py
	gofmt -w internal/emoji/generated.go

# The [keys] part of internal/config/default.toml, rendered from keySections
# (internal/config/keytable.go). A config test fails while the file is stale.
keys-doc:
	go generate ./internal/config/

# Pins + SHA-256 of LibreOffice dictionaries (downloads ~45 MB).
dict-manifest:
	python3 scripts/gen-dict-manifest.py
	gofmt -w internal/spell/dictionaries_generated.go

# Pins, hashes and pruned sizes of word-frequency lists (downloads ~180 MB).
freq-manifest:
	python3 scripts/gen-freq-manifest.py
	gofmt -w internal/spell/frequencies_generated.go

# Completion-model pins; hashes come from the HuggingFace API (no download).
model-manifest:
	python3 scripts/gen-model-manifest.py
	gofmt -w internal/llamacpp/models/models_generated.go

# Scan history (only when this dir is its own repo root) and the working tree.
secrets:
	@$(ENSURE) gitleaks
	@if [ -n "$(OWN_REPO)" ]; then \
		gitleaks git --redact --no-banner; \
	elif [ -n "$(CHECK_STRICT)" ]; then \
		echo "secrets: FAILED — no history to scan: $(CURDIR) is not the root of its own git repository." >&2; \
		exit 1; \
	else \
		echo "secrets: history scan SKIPPED — $(CURDIR) is not the root of its own git repository."; \
	fi
	gitleaks dir --redact --no-banner

# Supply-chain guard: every GitHub Actions `uses:` ref must stay SHA-pinned.
pin-check:
	bash scripts/actions-pin-check.sh

# The tools go.mod cannot pin, at tools/versions.env's versions (cheap when current).
ensure-tools:
	@$(ENSURE)

proto-check:
	@$(ENSURE) buf
	PROTO_CHECK_REQUIRED=1 bash scripts/proto-check.sh

# The Nix flake builds: flake.lock resolves and packaging/nix/package.nix's vendorHash
# matches the modules go.mod names. Both go stale silently (every dependency bump
# changes the hash; nix prints the new one). Without nix it is skipped with a warning,
# as the secret scan skips history outside a repository; CI sets REQUIRE_NIX.
nix-check:
	@if command -v nix >/dev/null 2>&1; then \
	  nix --extra-experimental-features 'nix-command flakes' build .#default --no-link --print-build-logs; \
	elif [ -n "$${REQUIRE_NIX:-}" ]; then \
	  echo "nix-check: nix is required here (REQUIRE_NIX is set)" >&2; exit 1; \
	else \
	  echo "nix-check: SKIPPED — nix is not installed; CI builds the flake"; \
	fi

# .goreleaser.yaml is valid, checked by the goreleaser the release runs.
release-check:
	@$(ENSURE) goreleaser
	goreleaser check

docs-check:
	@$(ENSURE) markdownlint
	markdownlint-cli2 "*.md" "docs/**/*.md" ".github/**/*.md"

# go.mod/go.sum are tidy and every module matches its checksum. Also in GoReleaser's
# before-hook, where a failure would come only after every other gate passed.
mod-check:
	go mod tidy -diff
	go mod verify

# Every `check` gate is a CI step, and every CI make target is a `check` gate.
ci-parity:
	bash scripts/ci-parity-check.sh

# The gate scripts' own tests (ensure-tools, ci-parity), offline.
script-check:
	bash scripts/test/tools-test.sh

# Every package must be named by a depguard rule, and no function may be reachable only
# from tests: tests build their own fixtures, and code no binary runs is not code.
arch-check: arch-quick
	@out=$$(CGO_ENABLED=0 go tool deadcode -tags=goolm ./cmd/...) || exit 1; \
	if [ -n "$$out" ]; then echo "$$out"; echo "arch-check: functions only tests reach (move them into a _test.go file, or delete them)"; exit 1; fi

# arch-check without deadcode's whole-program analysis (most of its time).
arch-quick:
	bash scripts/depguard-coverage-check.sh
	bash scripts/deps-check.sh
	go run scripts/testonly.go

# The plain and templated units must configure the same sandbox.
unit-check:
	bash scripts/unit-parity-check.sh

# Enable the tracked pre-commit/pre-push secret-scan hooks for this clone.
hooks:
	git config core.hooksPath .githooks
	@echo "hooks enabled: secrets + fmt + vet before a commit, secrets + make check before a push."
	@echo "Install gitleaks for full secret-scan coverage:"
	@echo "  https://github.com/gitleaks/gitleaks"

# The tools the workflows pin by hand (Dependabot does not bump them) against their
# latest releases; the weekly workflow runs it too. Needs network.
pins-outdated:
	bash scripts/pinned-tools.sh

# Print the pinned versions of the tool-directive tools (sanity check).
tools: ensure-tools
	@go version
	@go tool golangci-lint version
	@go tool govulncheck -version | head -2
	@goreleaser --version | grep -m1 GitVersion
	@echo "gitleaks $$(gitleaks version)"
	@echo "buf $$(buf --version)"
	@markdownlint-cli2 --help | head -1

# Test-coverage report: total summary to stdout + browsable HTML in coverage.html.
coverage:
	go test -count=1 -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1
	go tool cover -html=coverage.out -o coverage.html

# Floors in scripts/coverage-gate.sh; reuses the profile from `test`.
coverage-check: test
	bash scripts/coverage-gate.sh $(COVERPROFILE); status=$$?; rm -f $(COVERPROFILE); exit $$status

# The pre-push gate: what this machine can say in about a minute. The race detector and
# the coverage floors, cross builds, the vulnerability scan, deadcode, the Nix flake and
# the release config are CI's (`check`); pushing a tag runs them here too.
quick: mod-check compile test-quick lint fmt-check secrets pin-check unit-check arch-quick \
	ci-parity script-check proto-check docs-check

# Every CI gate, with CI's tools (ci-parity-check.sh holds the two lists equal).
# Non-mutating. CHECK_STRICT=1 also fails what would be skipped here.
check: mod-check compile coverage-check cross lint fmt-check vuln secrets pin-check unit-check \
	arch-check ci-parity script-check release-check proto-check docs-check nix-check

# ── Deploy ───────────────────────────────────────────────────────────────────
# Default install is per-user ($HOME/.local/bin); `make deploy PREFIX=/usr/local`
# for machine-wide (sudo only if BINDIR is not writable). Units get ExecStart
# rewritten to BINDIR by scripts/render-units.sh.
PREFIX  ?= $(HOME)/.local
BINDIR  ?= $(PREFIX)/bin
UNITDIR ?= $(HOME)/.config/systemd/user
UNIT    := kithd.service
TEMPLATE_UNIT := kithd@.service
APPDIR  ?= $(HOME)/.local/share/applications
DESKTOP := kith.desktop

# PROFILE=<name> targets kithd@<name>.service (same charset as config.go).
PROFILE ?=
ifneq ($(PROFILE),)
ifneq ($(shell printf '%s' '$(PROFILE)' | grep -Ex '[A-Za-z0-9._-]+'),$(PROFILE))
$(error PROFILE=$(PROFILE) may only contain letters, digits, dot, dash and underscore)
endif
DEPLOY_UNIT := kithd@$(PROFILE).service
PROFILE_FLAG := --profile $(PROFILE)
else
DEPLOY_UNIT := $(UNIT)
PROFILE_FLAG :=
endif

# systemd where available, launchd on macOS, otherwise binaries only.
UNAME_S := $(shell uname -s)
HAVE_SYSTEMCTL := $(shell command -v systemctl >/dev/null 2>&1 && echo yes)
LAUNCHAGENTDIR ?= $(HOME)/Library/LaunchAgents
PLIST_SRC   := packaging/macos/io.github.eugeneshtoka.kithd.plist
PLIST_LABEL := io.github.eugeneshtoka.kithd$(if $(PROFILE),.$(PROFILE))
PLIST       := $(LAUNCHAGENTDIR)/$(PLIST_LABEL).plist
PLIST_LOG   := kithd$(if $(PROFILE),.$(PROFILE)).log

install: build
	@set -eu; \
	if { [ -d "$(BINDIR)" ] && [ -w "$(BINDIR)" ]; } || mkdir -p "$(BINDIR)" 2>/dev/null; then SUDO=""; \
	else SUDO="sudo"; echo "==> $(BINDIR) is not writable — installing the binaries with sudo"; sudo mkdir -p "$(BINDIR)"; fi; \
	for b in kith kithd kith-mcp; do $$SUDO install -m755 "$$b" "$(BINDIR)/$$b"; done; \
	echo "==> binaries -> $(BINDIR)/kith, $(BINDIR)/kithd, $(BINDIR)/kith-mcp"
ifeq ($(HAVE_SYSTEMCTL),yes)
	@bash scripts/render-units.sh "$(BINDIR)" "$(UNITDIR)"
	@systemctl --user daemon-reload
	@echo "==> units    -> $(UNITDIR)/$(UNIT), $(UNITDIR)/$(TEMPLATE_UNIT)  (ExecStart=$(BINDIR)/kithd)"
else ifeq ($(UNAME_S),Darwin)
	@echo "==> no systemd on macOS — \`make deploy\` installs a LaunchAgent instead"
else
	@echo "==> no systemctl — systemd units skipped; start the daemon yourself: $(BINDIR)/kithd"
endif
ifneq ($(UNAME_S),Darwin)
	@install -d "$(APPDIR)"
	@sed 's|^Exec=.*|Exec=$(BINDIR)/kith --open %u|' packaging/desktop/$(DESKTOP) > "$(APPDIR)/$(DESKTOP)"
	@chmod 0644 "$(APPDIR)/$(DESKTOP)"
	@command -v update-desktop-database >/dev/null 2>&1 && update-desktop-database "$(APPDIR)" || true
	@command -v xdg-mime >/dev/null 2>&1 && xdg-mime default "$(DESKTOP)" x-scheme-handler/matrix || true
	@echo "==> handler  -> $(APPDIR)/$(DESKTOP)  (matrix: links open in kith)"
endif

# install + (re)start the daemon and wait for its socket.
#  - import only *set* session vars: importing an unset one clears it in the manager.
#  - restart, not start, so the new binary runs.
#  - the socket name hashes the MXID, so wait for any socket newer than a marker.
#    `activating` (auto-restart) is not failure: don't send users to re-login,
#    which costs the device its E2EE identity.
ifeq ($(HAVE_SYSTEMCTL),yes)
deploy: install
	@set -eu; \
	rt="$${XDG_RUNTIME_DIR:-/run/user/$$(id -u)}"; \
	for v in WAYLAND_DISPLAY DISPLAY DBUS_SESSION_BUS_ADDRESS; do \
	  printenv "$$v" >/dev/null 2>&1 && systemctl --user import-environment "$$v" || true; \
	done; \
	sockdir="$$rt/kith"; \
	marker=$$(mktemp "$${TMPDIR:-/tmp}/kith-deploy.XXXXXX"); \
	trap 'rm -f "$$marker"' EXIT; \
	systemctl --user enable "$(DEPLOY_UNIT)" >/dev/null; \
	systemctl --user restart "$(DEPLOY_UNIT)"; \
	sock=""; \
	i=0; while [ $$i -lt 240 ]; do \
	  sock=$$(find "$$sockdir" -maxdepth 1 -type s -newer "$$marker" 2>/dev/null | head -n 1); \
	  [ -n "$$sock" ] && break; \
	  case "$$(systemctl --user is-active "$(DEPLOY_UNIT)" 2>/dev/null)" in \
	    failed|inactive) break ;; \
	  esac; \
	  sleep 0.25; i=$$((i+1)); \
	done; \
	restarts=$$(systemctl --user show "$(DEPLOY_UNIT)" -p NRestarts --value 2>/dev/null || echo 0); \
	if systemctl --user is-active --quiet "$(DEPLOY_UNIT)" && [ -n "$$sock" ]; then \
	  echo "==> $(DEPLOY_UNIT) is up; socket $$sock"; \
	  echo "    run the client: $(BINDIR)/kith $(PROFILE_FLAG)"; \
	elif [ "$${restarts:-0}" -gt 0 ]; then \
	  echo "!!  $(DEPLOY_UNIT) is still coming up — $$restarts restart(s) so far. Last 20 lines:"; \
	  systemctl --user --no-pager --lines=20 status "$(DEPLOY_UNIT)" || true; \
	  echo "    The unit retries every 5s and never gives up, so this may right itself."; \
	  echo "    Watch it with:  journalctl --user -u $(DEPLOY_UNIT) -f"; \
	  exit 1; \
	else \
	  echo "!!  $(DEPLOY_UNIT) did not come up. Last 20 lines:"; \
	  systemctl --user --no-pager --lines=20 status "$(DEPLOY_UNIT)" || true; \
	  echo "    A daemon cannot log in — if it says there is no saved session, run:"; \
	  echo "      $(BINDIR)/kith login $(PROFILE_FLAG)"; \
	  exit 1; \
	fi
else ifeq ($(UNAME_S),Darwin)
# macOS: render the plist (label/args/log per PROFILE) and bootout+bootstrap.
deploy: install
	@set -eu; \
	install -d "$(LAUNCHAGENTDIR)" "$(HOME)/Library/Logs"; \
	sed -e 's|<string>io.github.eugeneshtoka.kithd</string>|<string>$(PLIST_LABEL)</string>|' \
	    -e 's|PATH="|PATH="$(BINDIR):|' \
	    -e 's|exec kithd |exec kithd $(PROFILE_FLAG) |' \
	    -e 's|kithd.log|$(PLIST_LOG)|' \
	    "$(PLIST_SRC)" > "$(PLIST)"; \
	chmod 0644 "$(PLIST)"; \
	uid=$$(id -u); \
	launchctl bootout "gui/$$uid/$(PLIST_LABEL)" 2>/dev/null || true; \
	launchctl bootstrap "gui/$$uid" "$(PLIST)"; \
	echo "==> LaunchAgent -> $(PLIST)"; \
	echo "    log: ~/Library/Logs/$(PLIST_LOG)"; \
	echo "    run the client: $(BINDIR)/kith $(PROFILE_FLAG)"; \
	echo "    no saved session yet? $(BINDIR)/kith login $(PROFILE_FLAG)"
else
deploy: install
	@echo "!!  no systemd and not macOS: nothing to enable. The binaries are in $(BINDIR);"
	@echo "    run the daemon under your own supervisor, e.g.  $(BINDIR)/kithd $(PROFILE_FLAG)"
endif

# Stop/disable every kithd unit (all profiles) and remove what `install` wrote.
# The matrix: association is removed only where it still names kith.desktop.
# Config, cache, crypto store and keyring session are kept.
undeploy:
	@set -eu; \
	if [ "$(HAVE_SYSTEMCTL)" = yes ]; then \
	  instances=$$( { systemctl --user list-units --all --plain --no-legend 'kithd@*.service' 2>/dev/null | awk '{print $$1}'; \
	    for f in "$(UNITDIR)"/*.wants/kithd@?*.service; do \
	      if [ -L "$$f" ] || [ -e "$$f" ]; then basename "$$f"; fi; \
	    done; } | sort -u ); \
	  for u in "$(UNIT)" $$instances; do \
	    systemctl --user disable --now "$$u" 2>/dev/null || true; \
	    case "$$u" in "$(UNIT)") ;; *) echo "==> stopped and disabled $$u" ;; esac; \
	  done; \
	  rm -f "$(UNITDIR)/$(UNIT)" "$(UNITDIR)/$(TEMPLATE_UNIT)"; \
	  for f in "$(UNITDIR)"/kithd-?*.service; do \
	    head -n1 "$$f" 2>/dev/null | grep -q '^# Written by kith for its config' || continue; \
	    u=$$(basename "$$f"); \
	    systemctl --user disable --now "$$u" 2>/dev/null || true; \
	    rm -f "$$f"; \
	    echo "==> stopped and removed $$u, which kith wrote for its own config"; \
	  done; \
	  systemctl --user daemon-reload; \
	  echo "==> removed $(UNITDIR)/$(UNIT), $(UNITDIR)/$(TEMPLATE_UNIT)"; \
	fi; \
	if [ "$(UNAME_S)" = Darwin ]; then \
	  uid=$$(id -u); \
	  for p in "$(LAUNCHAGENTDIR)"/io.github.eugeneshtoka.kithd*.plist; do \
	    [ -e "$$p" ] || continue; \
	    label=$$(basename "$$p" .plist); \
	    launchctl bootout "gui/$$uid/$$label" 2>/dev/null || true; \
	    rm -f "$$p"; \
	    echo "==> removed LaunchAgent $$label"; \
	  done; \
	fi; \
	if [ -e "$(BINDIR)/kith" ] || [ -e "$(BINDIR)/kithd" ] || [ -e "$(BINDIR)/kith-mcp" ]; then \
	  if [ -w "$(BINDIR)" ]; then SUDO=""; else SUDO="sudo"; fi; \
	  $$SUDO rm -f "$(BINDIR)/kith" "$(BINDIR)/kithd" "$(BINDIR)/kith-mcp"; \
	  echo "==> removed $(BINDIR)/kith, $(BINDIR)/kithd, $(BINDIR)/kith-mcp"; \
	fi; \
	if [ "$(UNAME_S)" != Darwin ]; then \
	  rm -f "$(APPDIR)/$(DESKTOP)"; \
	  command -v update-desktop-database >/dev/null 2>&1 && update-desktop-database "$(APPDIR)" || true; \
	  echo "==> removed $(APPDIR)/$(DESKTOP)"; \
	  for list in "$${XDG_CONFIG_HOME:-$$HOME/.config}/mimeapps.list" "$(APPDIR)/mimeapps.list"; do \
	    [ -f "$$list" ] || continue; \
	    grep -Eq '^x-scheme-handler/matrix=(.*;)?kith\.desktop(;|$$)' "$$list" || continue; \
	    sed -i -e '/^x-scheme-handler\/matrix=/{s/\([=;]\)kith\.desktop\(;\|$$\)/\1/;/^x-scheme-handler\/matrix=;*$$/d;}' "$$list"; \
	    echo "==> removed the matrix: handler association from $$list"; \
	  done; \
	fi; \
	echo "    config, cache, crypto store and the keyring session were left in place"

# ── Deployed-daemon operations ───────────────────────────────────────────────
# These honour PROFILE.
daemon-status:
	@systemctl --user --no-pager status "$(DEPLOY_UNIT)" || true

daemon-logs:
	@journalctl --user -u "$(DEPLOY_UNIT)" -n 100 --no-pager

daemon-restart:
	systemctl --user restart "$(DEPLOY_UNIT)"

daemon-stop:
	systemctl --user stop "$(DEPLOY_UNIT)"
