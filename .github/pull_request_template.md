<!--
Thanks for contributing to kith! Please keep PRs focused on one concern.
See CONTRIBUTING.md for the full guide.
-->

## What does this change?

<!-- A short description of the change and the motivation behind it. -->

## Related issues

<!-- e.g. "Closes #123". Delete if not applicable. -->

## Checklist

- [ ] `make check` passes locally (see [CONTRIBUTING.md](../CONTRIBUTING.md) for every gate it runs)
- [ ] Tests added or updated for behavioral changes
- [ ] Concurrency, merging, caching or scope touched? Failure review done ([CONTRIBUTING.md](../CONTRIBUTING.md#risky-changes-get-a-failure-review)): what the class test never generated, what was added (failing first), what was left

## Failure review

<!-- For a risky change (see above): what was asked, what was added to the generator, and what was left and why. Delete otherwise. -->
- [ ] Commit messages follow [Conventional Commits](https://www.conventionalcommits.org) (`feat:`, `fix:`, `docs:`, …)
- [ ] Docs updated if user-facing behavior or config changed (README, `docs/`, `internal/config/default.toml`, ARCHITECTURE.md)
- [ ] Layer boundaries respected (depguard rules in `.golangci.yml`) — no SDK/db/matrix imports leaking into `domain`/`tui`
