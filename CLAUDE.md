# kith

The process is in [CONTRIBUTING.md](CONTRIBUTING.md): gates (`make check` = CI),
layering (depguard), schema and protocol changes. Two rules there are the ones most
often skipped:

- **Risky changes get a failure review** before they count as done
  ([CONTRIBUTING.md](CONTRIBUTING.md#risky-changes-get-a-failure-review)). If a change
  is concurrent, merges more than one writer's data, caches a derived result, or
  enforces scope, ask what its class test never generates, add those to the generator,
  see each fail on the old code, mutation-check the fix, and report what was left.
  Do this before marking a plan item done, not after an audit finds it.
- **Tests don't shape code.** No production seam, field or schema exists only for a
  test; tests build their own fixtures.

Working notes (audits, plans, recaps) live in `notes/`, which is gitignored; public
docs live in `docs/`.
