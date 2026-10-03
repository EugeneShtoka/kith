#!/usr/bin/env bash
#
# npm-audit.sh — npm audit of markdownlint's lock (the only npm tree in the repo),
# failing on every advisory except those named in tools/markdownlint/audit-allow.json,
# each with a reason and an expiry date: an allowed advisory past its date fails too,
# so an exception is revisited rather than forgotten.

set -euo pipefail

dir=tools/markdownlint
cd "$dir"
# npm audit exits non-zero whenever it finds anything; the verdict is ours.
report=$(npm audit --json --omit=dev || true)

REPORT="$report" node -e '
const report = JSON.parse(process.env.REPORT);
const allow = require("./audit-allow.json");
const today = new Date().toISOString().slice(0, 10);
const found = new Map();
for (const [pkg, vuln] of Object.entries(report.vulnerabilities || {})) {
  for (const via of vuln.via) {
    if (typeof via !== "object") continue; // a dependency path, reported under its own package
    const id = (via.url || "").split("/").pop();
    found.set(id, { pkg: via.name || pkg, severity: via.severity, title: via.title, url: via.url });
  }
}
let failed = false;
for (const [id, v] of found) {
  const entry = allow.find((a) => a.advisory === id);
  if (!entry) {
    console.error(`npm-audit: ${v.severity} ${id} in ${v.pkg}: ${v.title} (${v.url})`);
    failed = true;
  } else if (entry.until < today) {
    console.error(`npm-audit: the exception for ${id} (${v.pkg}) expired on ${entry.until}; check for a fixed release, or renew it with a reason`);
    failed = true;
  } else {
    console.log(`npm-audit: allowed until ${entry.until}: ${id} in ${v.pkg}`);
  }
}
for (const entry of allow) {
  if (!found.has(entry.advisory)) {
    console.error(`npm-audit: ${entry.advisory} (${entry.package}) is no longer reported; remove it from audit-allow.json`);
    failed = true;
  }
}
if (failed) process.exit(1);
console.log("npm-audit: OK");
'
