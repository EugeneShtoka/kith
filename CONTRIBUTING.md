# Contributing to kith

Thanks for your interest in improving kith. This guide covers how to build, test,
and submit changes. For how the code is laid out, read
[ARCHITECTURE.md](ARCHITECTURE.md) first; for the user-facing documentation, start at
[docs/README.md](docs/README.md).

## Prerequisites

- **Go 1.26.3 or newer** to start `make`. Every gate then runs on exactly go.mod's
  `toolchain` line (go1.26.8), which CI and the release build with too: the Makefile
  sets `GOTOOLCHAIN` to it (downloaded once) and `GOENV=off`, so neither a newer local
  go nor a user `go env` setting (a `GOEXPERIMENT`, say) changes what is checked.
  `GOTOOLCHAIN=local make …` opts out, for offline work.
- **Node.js 20 or newer with npm**, for `docs-check` (markdownlint) and `vuln` (npm
  audit of markdownlint's lock).
- **An account on a network kith speaks** (Matrix, WhatsApp, Telegram or Slack) to
  run the client end to end. Use a test account if you can: the client's
  diagnostics drive a real session.
- **Linux with a user systemd** for `make deploy`. Building and testing need nothing
  beyond Go.

You install nothing else. The tools the gates run are pinned in one place, and CI
installs them the same way:

| tool | pinned by | installed |
| --- | --- | --- |
| golangci-lint, govulncheck, the protoc plugins | the `tool` directive in `go.mod` | run through `go tool` |
| goreleaser, gitleaks, buf | `tools/versions.env` (version and SHA-256 per platform) | `scripts/ensure-tools.sh`, into `.tools/` (gitignored), by the gate that needs it |
| markdownlint-cli2 | `tools/markdownlint/package-lock.json` | `npm ci` into `.tools/` |

A tool is fetched only when its pinned version is not already in `.tools/`, and an
archive whose checksum differs is refused before it is unpacked. `make pins-outdated`
lists the pins behind their latest release (a weekly workflow does too); a bump
changes the version and its checksums in `tools/versions.env`.

Also optional: Python 3 with [pyte](https://github.com/selectel/pyte) for
`tools/pty-drive.py`. No gate needs it.

## First-time setup

```sh
make hooks   # point core.hooksPath at .githooks (secrets + fmt + vet on commit, make quick on push)
make tools   # install the pinned tools and print every version the gates use
```

Git does not clone hooks, so `make hooks` is opt-in per checkout. The pre-commit hook
scans staged changes for secrets, then, when Go files are staged, runs the format check
and `go vet` on the staged snapshot (not the working tree, so unstaged edits can neither
hide nor cause a failure). The pre-push hook scans history for secrets, then runs `make
quick` (below): the gates that answer in about a minute. Pushing a tag, or
`KITH_FULL_CHECK=1 git push`, runs `make check CHECK_STRICT=1` instead, every CI gate.
`--no-verify` skips a hook.

## The `goolm` build tag

mautrix-go uses libolm through cgo unless the **`goolm`** build tag selects its
pure-Go implementation. This project always builds with it, so no binary needs cgo.
It is applied in four places, and they must agree:

- the `Makefile` exports `GOFLAGS := -tags=goolm`, so every `make` target gets it;
- `.golangci.yml` sets `run.build-tags: [goolm]`;
- the CI workflow sets `GOFLAGS: -tags=goolm` for every job;
- `.goreleaser.yaml` passes `-tags=goolm` to each release build.

Every binary is built with `CGO_ENABLED=0`: `make build`, `make install`, `make run`,
CI's build and cross-compile jobs, and each release build. That matters beyond libolm.
With cgo on, mautrix's `cryptohelper` links `mattn/go-sqlite3` (a C SQLite, through
`go.mau.fi/util/dbutil/litestream`), although only the pure-Go `modernc.org/sqlite`
driver is ever opened. Tests are the exception: `-race` needs cgo, so `make test` and
CI's test step build with it.

Running `go` directly, outside `make`, set it yourself:

```sh
export GOFLAGS=-tags=goolm
go test ./internal/db/
```

Without the tag the build pulls in cgo and libolm, and the linter analyzes files the
release never compiles.

## Building and running

kith is **three binaries**: the always-on `kithd` daemon, which owns the Matrix
session, the cache, the crypto store and the notification decision; the `kith`
client, which attaches to it over a unix socket; and `kith-mcp`, an MCP server that is
a third client of the same socket. There is no in-process fallback.

```sh
make build        # ./kith, ./kithd and ./kith-mcp
make build-tui    # just one of them: build-tui, build-daemon, build-mcp
make run          # go run the client; it spawns a daemon if none is running
make run-daemon   # run the daemon in the foreground to watch its log
make deploy       # install to ~/.local/bin, install the user unit, (re)start it
make undeploy     # stop and remove the unit and the kith/kithd binaries; data stays
make daemon-logs  # the last 100 lines of the daemon's journal
```

Two things that catch people out:

- `make run` spawns the **installed** daemon if there is one. After changing daemon
  code, run `make deploy`, or run `make run-daemon` in a second terminal first.
- `make run-daemon` exits 0 immediately if the systemd unit is already serving: the
  single-instance lock is held. Stop the unit first with `make daemon-stop`.

`make deploy PREFIX=/usr/local` installs machine-wide instead; see the `Makefile`
for the details. User-facing setup is in [docs/getting-started.md](docs/getting-started.md).

## Before you open a PR

Run the full local gate. It must pass:

```sh
make check
```

`make check` runs every CI gate, with CI's toolchain and tools. CI's steps are these
same targets, and the `ci-parity` gate fails when the two lists differ or when a CI step
calls a tool directly instead of through its target. In order:

| target | what it checks |
| --- | --- |
| `mod-check` | `go mod tidy -diff` and `go mod verify` |
| `compile` | every package builds as released (`CGO_ENABLED=0`) |
| `coverage-check` | `test` (`go test -race -count=1 -coverprofile=… ./...`; the profile goes to `$TMPDIR/kith-cover-<pid>.out`, or `COVERPROFILE=path`), then the floors in `scripts/coverage-gate.sh` |
| `cross` | every package builds for linux, darwin, freebsd and windows on amd64 and arm64 (`CROSS_TARGETS`) |
| `lint` | `golangci-lint run`, including the depguard layer rules |
| `fmt-check` | `golangci-lint fmt --diff` (gofmt + goimports), non-mutating |
| `vuln` | `govulncheck ./...`, and `npm audit` of markdownlint's lock (`scripts/npm-audit.sh`: an advisory with no fixed release can be allowed in `tools/markdownlint/audit-allow.json`, with a reason and an expiry date) |
| `secrets` | gitleaks over the committed history **and** the working tree |
| `pin-check` | every GitHub Actions `uses:` is pinned to a commit SHA |
| `unit-check` | the plain and templated systemd units configure the same sandbox |
| `arch-check` | every package under `internal/` and `cmd/`, at any depth, is named by a depguard rule; `scripts/deps-check.sh`: the TUI, the wire and `kith-mcp` link no daemon-side package at any depth; `go tool deadcode` and `scripts/testonly.go`: no function exists only for tests (neither sees a field or var that does, which review must) |
| `ci-parity` | CI runs exactly these targets (`scripts/ci-parity-check.sh`) |
| `script-check` | the tests of those gate scripts (`scripts/test/tools-test.sh`): a tampered or missing tool download, and each way CI and `check` can drift |
| `release-check` | `goreleaser check` over `.goreleaser.yaml` |
| `proto-check` | `buf lint`, then `buf generate` into a scratch dir matches `internal/api/backend` file for file |
| `docs-check` | markdownlint over `*.md`, `docs/**/*.md` and `.github/**/*.md` |

Nothing is skipped for want of a tool. The one gate that can be skipped is the history
secret scan, when the tree is not the root of its own git repository; `CHECK_STRICT=1`
(the pre-push hook, and CI) makes that a failure too.

`make quick`, the pre-push gate, is the same list less what costs minutes: tests run
without `-race` and from Go's test cache (`test-quick`), with no coverage floors;
`arch-quick` is `arch-check` without `deadcode`; and `cross`, `vuln`, `release-check` and
`nix-check` are left to CI. In CI, `cross` and `nix-check` run only for a release tag (one job, `release-builds`);
the weekly workflow (`.github/workflows/vuln-weekly.yml`) builds the Nix flake too, so
a stale `vendorHash` — it changes whenever a package is newly imported, not only when
`go.mod` does — shows within a week. Its failure prints the hash to put in
`packaging/nix/package.nix`.

`govulncheck` also runs weekly on its own (`.github/workflows/vuln-weekly.yml`), so a new
advisory is found even when nobody pushes.

Each target also runs on its own (`make lint`, `make vuln`, …). Two more are useful:

```sh
make fmt       # format in place (fmt-check only reports)
make fix       # go mod tidy + go fmt + golangci-lint --fix
make coverage  # coverage summary, plus coverage.html to browse
```

Non-Go files (YAML, shell, Markdown, TOML) have no formatter in the gate, so their
indentation is stated in `.editorconfig`.

### Tests

**Add tests for behavioral changes.** The codebase favors table-driven, behavioral
tests that assert on state rather than on rendered glyph strings. The TUI tests run
against the `internal/apitest` fakes rather than a real backend. Pure logic lives in
`internal/domain`. The coverage gate enforces an overall floor for hand-written code
and a per-package floor; the numbers and the exemptions are in
`scripts/coverage-gate.sh`.

### Risky changes get a failure review

Audits showed where bugs come from: in four of five, the worst defect was in a
mechanism the previous change had just added, and its tests covered the happy paths.
So a change in one of these four classes is not done until its test has been asked
what it never generates:

| The change… | Its class test | Typical blind spots |
| --- | --- | --- |
| runs **concurrently** (goroutines, commands, retries, shutdown) | `-race`, plus a model test where one fits | a reply lost after the write landed; a call that fails before or after commit; cancellation mid-call; every exit route (quit, interrupt, SIGTERM, SIGHUP) |
| **merges** what more than one writer wrote (drafts, edits, redactions) | `TestDraftsSurviveEveryInterleaving`, `TestTheCacheAndTheClientResolveHistoryAlike` | the assistant writing between the RPCs of one save; a window taking over (the old one steps aside); faults injected; ties; a deletion elsewhere; the next start restoring state |
| **caches** a derived result (row cache, memos, fingerprints) | `TestTheRowCacheHoldsOverAnyUpdateSequence`, `TestTheRowRendererReadsOnlyKeyedState` | an input the render reads but the key does not hold; a count standing in for content; state that changes with no message changing |
| **enforces scope or policy** (kith-mcp `[agent]`, model opt-in) | `TestAFailedLookupNeverWidensTheScope` | a lookup that fails; an unknown value read as a default; a list entry kind the fallback did not consider |

The review is short and adversarial:

1. **Ask.** List the inputs and failures the class test does not produce for this
   change. The table's third column is where to start, not the whole list.
2. **Generate.** Add each to the test's generator (a fault, a step, a second writer, a
   row in a table the test checks), not as a one-off case beside it.
3. **See it fail.** Each addition must fail on the code before the fix, or be shown
   already covered. Mutation-check the fix: break it and watch the test fail.
4. **Write it down.** Say in the PR what was asked, what was added, and what was left,
   and why. "Nothing found" is an answer too.

A new mechanism in one of these classes without a class test gets one in the same PR.
Prefer a generator that is complete by construction (one comparable key struct, an AST
check of what is read, a property over every failure) over a list of named cases: the
next bug is the case nobody named.

Full audits run at milestones only: the first green CI run, before a release, and
after a change that cuts across layers. Between them, these reviews carry the load.

### Layering

The layer boundaries are enforced by depguard in `.golangci.yml`, and a PR that
crosses them fails lint. A **new package** needs a rule of its own before
`make arch-check` passes: decide what it may import and write it down. The rules and
their reasons are summarized in [ARCHITECTURE.md](ARCHITECTURE.md#layered-design).

### Schema changes

If you touch `internal/db/schema.go`, read [docs/database.md](docs/database.md)
first. In short: change `baseSchema`, append a guarded migration, add its row to the
fingerprint ledger, and never edit a shipped one. An index needs only the
`baseSchema` change.

### Protocol changes

The daemon's wire types are generated from `api/proto/backend/v1/*.proto`. After
editing a `.proto`:

```sh
make proto   # buf lint + buf generate into internal/api/backend/v1
```

Commit the generated code with the schema change. Domain↔proto conversion lives in
one place, `internal/api/backend/v1/protoconv`; add the mapping there and a round-trip
test beside it.

### Other generated files

These are generated and committed, so a build needs no network. Regenerate them only
when you mean to change what they pin:

```sh
make emoji           # internal/tui/emoji_generated.go, from Unicode's data
make dict-manifest   # internal/spell/dictionaries_generated.go (downloads ~45 MB to hash)
make freq-manifest   # internal/spell/frequencies_generated.go (downloads ~180 MB)
make model-manifest  # internal/llamacpp/models/models_generated.go (reads hashes, downloads nothing)
```

## Diagnostics that a test cannot replace

Two tools answer questions unit tests structurally cannot, both about the gap between
what this client measures and what a terminal draws. Reach for them when the screen
shows something the model does not explain: rows duplicated, a line from elsewhere
left standing, a pane border in pieces.

```sh
go run ./cmd/emoji-probe                 # every offered emoji, measured vs drawn
go run ./cmd/emoji-probe -text "…"       # one line or one glyph, same question
tools/pty-drive.py --keys j,j,tab,l,esc  # run the real client, print the cell grid
```

`emoji-probe` asks the terminal directly (print a glyph, ask where the cursor landed),
so it needs a real tty; `-set` and `-tone` pick the emoji set and skin tone.
`pty-drive.py` runs the client in a pseudo-terminal and renders its output through a
terminal emulator, so the grid can be inspected without a screenshot; it exits
non-zero if any row is drawn twice. It needs `pyte`, runs `~/.local/bin/kith` unless
you pass `--cmd`, and drives the **real account**: keys that open rooms send read
receipts.

## Commit messages

Use [Conventional Commits](https://www.conventionalcommits.org). The release notes are
generated from commit prefixes, so this matters:

- `feat: ...` for a new user-facing capability (grouped under **Features**)
- `fix: ...` for a bug fix (grouped under **Bug fixes**)
- `docs:`, `test:`, `chore:`, `ci:`, `refactor:` for everything else

There is no `CHANGELOG.md`, and that is deliberate: GoReleaser builds the release
notes from these prefixes at tag time, and a hand-maintained file would be a second
copy that can disagree with the commits it summarizes.

Keep commits **granular and self-explanatory**: one logical change per commit, with
the *why* in the body when the diff does not make it obvious.

## Pull requests

1. Fork and branch off `main`.
2. Keep the change focused: one concern per PR where practical.
3. Make sure `make check` is green and tests cover the change.
4. Open the PR against `main` and fill in the template. Its checklist asks for:
   - `make check` passing locally;
   - tests added or updated for behavioral changes;
   - Conventional Commit messages;
   - docs updated if user-facing behavior or configuration changed (the README,
     [docs/](docs/README.md), or ARCHITECTURE.md);
   - layer boundaries respected (no SDK, `db` or `matrix` imports leaking into
     `domain` or `tui`).

CI runs on every PR and must pass before merge. A single `ci-ok` job depends on every
gate, so branch protection needs to require only that one.

## Releases and versioning

Releases follow [Semantic Versioning](https://semver.org), `v`-prefixed
(`vMAJOR.MINOR.PATCH`). Maintainers cut a release by pushing a `v*` tag. The release
workflow first re-runs the whole CI gate set against the tagged commit, and only then
does GoReleaser build the binaries and checksums, the Linux packages, and the AUR
and Homebrew updates. Contributors don't need to bump versions. What each publisher
needs, and how to test the packages locally, is in
[docs/packaging.md](docs/packaging.md).

If you change `packaging/systemd/`, keep both units in step: `make unit-check` fails
when they disagree, or when a `ReadWritePaths=` directory is missing from the
`ExecStartPre=+mkdir` line that creates it.

## Security

Please report vulnerabilities privately, not in an issue. See
[SECURITY.md](SECURITY.md).

## License

By contributing, you agree that your contributions are licensed under the project's
license, the [GNU Affero General Public License, version 3 or later](LICENSE)
(AGPL-3.0-or-later).
