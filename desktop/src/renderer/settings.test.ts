import { clone, create } from "@bufbuild/protobuf";
import fc from "fast-check";
import { describe, expect, it } from "vitest";

import { type DeviceSettings, DeviceSettingsSchema } from "../gen/device/v1/device_pb";
import { change, type Changed, changeTries, type Device, type Key, type Read, same, withKey } from "./settings";

/**
 * File is device.toml behind the relay: written only at the revision it is at (else
 * ABORTED, as main passes it on), each write a new revision. before runs as each write
 * reaches it: another writer, between the page's read and its write.
 */
class File implements Device {
  revision = 1;
  writes = 0;
  reads = 0;
  readonly before: (() => void)[] = [];

  constructor(public settings: DeviceSettings) {}

  /** other is another writer setting key, as the TUI's editor or a hand edit does. */
  other<K extends Key>(key: K, value: DeviceSettings[K]): void {
    this.settings = withKey(this.settings, key, value);
    this.revision++;
  }

  read(): Promise<Read> {
    this.reads++;
    return Promise.resolve({ settings: clone(DeviceSettingsSchema, this.settings), revision: String(this.revision) });
  }

  write(base: string, settings: DeviceSettings): Promise<Read> {
    this.before.shift()?.();
    if (base !== String(this.revision)) {
      return Promise.reject(
        new Error(
          "Error invoking remote method 'kith:call': ConnectError: [aborted] device.toml changed since it was read",
        ),
      );
    }
    this.writes++;
    this.settings = clone(DeviceSettingsSchema, settings);
    this.revision++;
    return this.read();
  }
}

const defaults = create(DeviceSettingsSchema, {
  sendReceipts: true,
  unreadLine: true,
  markdown: true,
  confirmSms: true,
  sendTyping: true,
  emojiSet: "curated",
  reactionsScope: "room",
});

/** shownBy is what a page that read file shows. */
function shownBy(file: File): Read {
  return { settings: clone(DeviceSettingsSchema, file.settings), revision: String(file.revision) };
}

describe("a change on the settings page", () => {
  it("is written at the revision read, and the page shows the answer", async () => {
    const file = new File(defaults);
    const got = await change(file, shownBy(file), "sendReceipts", false);
    expect(got.kept).toBe(false);
    expect(file.settings.sendReceipts).toBe(false);
    expect(got.revision).toBe(String(file.revision));
    expect(file.writes).toBe(1);
  });

  it("is written again onto a file another writer changed meanwhile, both changes kept", async () => {
    const file = new File(defaults);
    const shown = shownBy(file);
    file.before.push(() => {
      file.other("markdown", false); // by hand
    });
    const got = await change(file, shown, "skinTone", "\u{1F3FB}");
    expect(got.kept).toBe(false);
    expect([file.settings.markdown, file.settings.skinTone]).toEqual([false, "\u{1F3FB}"]);
    expect(got.settings.markdown).toBe(false);
  });

  it("gives way to another writer's change of the same setting, and says so", async () => {
    const file = new File(defaults);
    const shown = shownBy(file);
    file.before.push(() => {
      file.other("reactionsStatic", ["🎉"]); // the TUI's editor
    });
    const got = await change(file, shown, "reactionsStatic", ["👍"]);
    expect(got.kept).toBe(true);
    expect(file.settings.reactionsStatic).toEqual(["🎉"]);
    expect(got.settings.reactionsStatic).toEqual(["🎉"]);
    expect(file.writes).toBe(0);
  });

  it("gives up after a few tries, each lost to another writer", async () => {
    const file = new File(defaults);
    for (let i = 0; i < changeTries; i++) {
      file.before.push(() => {
        file.other("sendTyping", i % 2 === 0);
      });
    }
    await expect(change(file, shownBy(file), "unreadLine", false)).rejects.toThrow("[aborted]");
    expect(file.settings.unreadLine).toBe(true);
    expect(file.reads).toBe(changeTries - 1);
  });

  it("is not tried again after a failure other than another writer's", async () => {
    const file = new File(defaults);
    file.write = () => Promise.reject(new Error("ConnectError: [invalid_argument] display.skin_tone: no tone"));
    await expect(change(file, shownBy(file), "skinTone", "x")).rejects.toThrow("invalid_argument");
    expect(file.reads).toBe(0);
  });

  it("tells settings apart as the relay does: a map in any order, a list in order, a rule's send unset or set", () => {
    expect(same("emojiExtra", { a: "1", b: "2" }, { b: "2", a: "1" })).toBe(true);
    expect(same("reactionsStatic", ["a", "b"], ["b", "a"])).toBe(false);
    const rule = (send?: boolean) => [
      { $typeName: "device.v1.ReadRule" as const, match: "space:Work", sender: "", send },
    ];
    expect(same("readRules", rule(), rule(false))).toBe(false);
    expect(same("readRules", rule(true), rule(true))).toBe(true);
  });
});

// The values a generated writer gives each setting it changes.
const values: { [K in Key]: fc.Arbitrary<DeviceSettings[K]> } = {
  sendReceipts: fc.boolean(),
  unreadLine: fc.boolean(),
  markdown: fc.boolean(),
  confirmSms: fc.boolean(),
  sendTyping: fc.boolean(),
  skinTone: fc.constantFrom("", "\u{1F3FB}", "\u{1F3FF}"),
  emojiSet: fc.constantFrom("curated", "standard", "complete"),
  emojiExtra: fc.dictionary(fc.constantFrom("a", "b"), fc.constantFrom("1", "2"), { maxKeys: 2 }),
  reactionsScope: fc.constantFrom("room", "space", "global", "static"),
  reactionsStatic: fc.array(fc.constantFrom("👍", "🎉"), { maxLength: 2 }),
  readRules: fc.array(
    fc.record({
      $typeName: fc.constant("device.v1.ReadRule" as const),
      match: fc.constantFrom("", "space:Work"),
      sender: fc.constantFrom("", "@a:x"),
      send: fc.option(fc.boolean(), { nil: undefined }),
    }),
    { maxLength: 2 },
  ),
};
const keys = Object.keys(values) as Key[];

/** A setting and a value for it. */
const setting = fc.constantFrom(...keys).chain((key) => values[key].map((value) => ({ key, value })));

describe("the settings page and another writer of device.toml", () => {
  it("never loses either's change, nor writes over a setting it was not shown", async () => {
    await fc.assert(
      fc.asyncProperty(
        setting,
        // Before each write of the page's: none, or another writer's changes.
        fc.array(fc.array(setting, { maxLength: 2 }), { maxLength: changeTries + 1 }),
        async (mine, others) => {
          const file = new File(defaults);
          const shown = shownBy(file);
          const theirs = new Map<Key, unknown>();
          for (const changes of others) {
            file.before.push(() => {
              for (const c of changes) {
                file.other(c.key, c.value);
                theirs.set(c.key, c.value);
              }
            });
          }
          let got: Changed | undefined;
          try {
            got = await change(file, shown, mine.key, mine.value);
          } catch {
            // Given up, only with each try lost to another writer: the page's change is
            // not written, every other writer's is.
            expect(others.slice(0, changeTries).filter((c) => c.length > 0)).toHaveLength(changeTries);
            expect(file.writes).toBe(0);
          }
          for (const key of keys) {
            const want = theirs.has(key) ? theirs.get(key) : shown.settings[key];
            if (key !== mine.key) {
              expect(same(key, file.settings[key], want as never), key).toBe(true);
            }
          }
          const changedElsewhere =
            theirs.has(mine.key) && !same(mine.key, theirs.get(mine.key) as never, shown.settings[mine.key]);
          if (got === undefined) {
            return;
          }
          // The page's setting: its own, unless another writer changed it first, whose
          // value is kept then, and said to be.
          if (changedElsewhere) {
            expect(same(mine.key, file.settings[mine.key], theirs.get(mine.key) as never)).toBe(true);
            expect(got.kept).toBe(!same(mine.key, file.settings[mine.key], mine.value));
          } else {
            expect(same(mine.key, file.settings[mine.key], mine.value)).toBe(true);
            expect(got.kept).toBe(false);
          }
          expect(same(mine.key, got.settings[mine.key], file.settings[mine.key])).toBe(true);
          expect(got.revision).toBe(String(file.revision));
        },
      ),
      { numRuns: 500 },
    );
  });
});
