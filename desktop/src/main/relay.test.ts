import { chmodSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";

import { afterEach, describe, expect, it } from "vitest";

import { startRelay } from "./relay";

const dirs: string[] = [];

afterEach(() => {
  for (const dir of dirs.splice(0)) {
    rmSync(dir, { recursive: true, force: true });
  }
});

/** fakeRelay is an executable running script under node, as kith-relay would run. */
function fakeRelay(script: string): string {
  const dir = mkdtempSync(path.join(tmpdir(), "kith-relay-"));
  dirs.push(dir);
  const file = path.join(dir, "kith-relay");
  writeFileSync(file, `#!${process.execPath}\n${script}\n`);
  chmodSync(file, 0o700);
  return file;
}

describe("starting kith-relay", () => {
  it("resolves with the socket it printed, and the relay leaves when its stdin closes", async () => {
    const relay = await startRelay(
      fakeRelay(
        `console.log("/run/kith/relay-x/relay.sock"); process.stdin.resume(); process.stdin.on("end", () => process.exit(0));`,
      ),
    );
    expect(relay.socket).toBe("/run/kith/relay-x/relay.sock");
    const exited = new Promise<number | null>((resolve) => relay.process.once("exit", resolve));
    relay.process.stdin?.end();
    expect(await exited).toBe(0);
  });

  it("rejects when the relay leaves before it listens", async () => {
    await expect(startRelay(fakeRelay(`console.error("no config"); process.exit(3);`))).rejects.toThrow(/exited \(3\)/);
  });

  it("rejects when there is no relay to run", async () => {
    await expect(startRelay(path.join(tmpdir(), "no-such-kith-relay"))).rejects.toThrow(/ENOENT/);
  });
});
