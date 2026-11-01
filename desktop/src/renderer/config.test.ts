import { clone, create, type MessageInitShape } from "@bufbuild/protobuf";
import fc from "fast-check";
import { describe, expect, it } from "vitest";

import {
  type ConfigSetting,
  ConfigSettingKind,
  ConfigSettingSchema,
  type ConfigSettingsResponse,
  type ConfigValue,
  type SetConfigSettingResponse,
} from "../gen/backend/v1/services_pb";
import {
  type Bridge,
  change,
  type Changed,
  flag,
  grouped,
  list,
  number,
  same,
  text,
  typed,
  valueAt,
  words,
} from "./config";
import { changeTries } from "./settings";

const setting = (init: MessageInitShape<typeof ConfigSettingSchema>) => create(ConfigSettingSchema, init);

/** The settings the fake lists: one of each kind. */
const listed: ConfigSetting[] = [
  setting({
    path: "display.deleted.keep",
    group: "display",
    label: "Keep",
    kind: ConfigSettingKind.TOGGLE,
    fallback: flag(false),
  }),
  setting({
    path: "notifications.max_per_room",
    group: "notifications",
    label: "Popups",
    kind: ConfigSettingKind.NUMBER,
    least: 0n,
    fallback: number(0n),
  }),
  setting({
    path: "display.unread",
    group: "display",
    label: "Unread",
    kind: ConfigSettingKind.CHOICE,
    choices: [{ value: "messages" }, { value: "notifications" }],
    fallback: text("messages"),
  }),
  setting({
    path: "notifications.rate_window",
    group: "notifications",
    label: "Within",
    kind: ConfigSettingKind.DURATION,
    fallback: text("2m"),
  }),
  setting({
    path: "codes.symbols",
    group: "codes",
    label: "Symbols",
    kind: ConfigSettingKind.TEXT,
    fallback: text(""),
  }),
  setting({
    path: "display.tracked.words",
    group: "display",
    label: "Tracked",
    kind: ConfigSettingKind.LIST,
    fallback: list([]),
  }),
];

/**
 * File is config.toml behind kithd: a setting is set only at the revision the file is at
 * (else ABORTED, as main passes it on), each change a new revision, one changing nothing
 * none. before runs as each set reaches it: another writer between the page's read and
 * its set.
 */
class File implements Bridge {
  revision = 1;
  writes = 0;
  reads = 0;
  readonly before: (() => void)[] = [];
  readonly settings = listed.map((s) => clone(ConfigSettingSchema, s));

  /** other is another writer setting path: the TUI's settings screen, a hand edit, another window. */
  other(path: string, value: ConfigValue | undefined): void {
    const s = this.settings.find((x) => x.path === path);
    if (s !== undefined) {
      s.value = value;
    }
    this.revision++;
  }

  read(): Promise<ConfigSettingsResponse> {
    this.reads++;
    return Promise.resolve(this.shown());
  }

  shown(): ConfigSettingsResponse {
    return {
      $typeName: "backend.v1.ConfigSettingsResponse",
      revision: String(this.revision),
      settings: this.settings.map((s) => clone(ConfigSettingSchema, s)),
    };
  }

  write(base: string, path: string, value: ConfigValue | undefined): Promise<SetConfigSettingResponse> {
    this.before.shift()?.();
    if (base !== String(this.revision)) {
      return Promise.reject(
        new Error("Error invoking remote method 'kith:call': ConnectError: [aborted] config moved since it was read"),
      );
    }
    const s = this.settings.find((x) => x.path === path);
    if (s === undefined) {
      return Promise.reject(new Error("ConnectError: [permission_denied] not a setting set here"));
    }
    if (!same(s.value, value)) {
      this.writes++;
      s.value = value;
      this.revision++;
    }
    return Promise.resolve({
      $typeName: "backend.v1.SetConfigSettingResponse",
      revision: String(this.revision),
      setting: clone(ConfigSettingSchema, s),
    });
  }
}

describe("a change of the bridge's settings", () => {
  it("is set at the revision read, and the page shows kithd's answer", async () => {
    const file = new File();
    const got = await change(file, file.shown(), "display.unread", text("notifications"));
    expect(got.kept).toBe(false);
    expect(words(valueAt(got.read, "display.unread"))).toBe("notifications");
    expect(got.read.revision).toBe(String(file.revision));
    expect(file.writes).toBe(1);
  });

  it("is set again onto a file another writer changed meanwhile, both changes kept", async () => {
    const file = new File();
    const shown = file.shown();
    file.before.push(() => {
      file.other("codes.symbols", text("-")); // the TUI's settings screen
    });
    const got = await change(file, shown, "display.deleted.keep", flag(true));
    expect(got.kept).toBe(false);
    expect(words(valueAt(got.read, "codes.symbols"))).toBe("-");
    expect(words(valueAt(got.read, "display.deleted.keep"))).toBe("on");
  });

  it("gives way to another writer's change of the same setting, and says so", async () => {
    const file = new File();
    const shown = file.shown();
    file.before.push(() => {
      file.other("display.tracked.words", list(["deploy"])); // by hand
    });
    const got = await change(file, shown, "display.tracked.words", list(["release"]));
    expect(got.kept).toBe(true);
    expect(words(valueAt(got.read, "display.tracked.words"))).toBe("deploy");
    expect(file.writes).toBe(0);
  });

  it("clears a setting, and a clear another writer made first is no conflict", async () => {
    const file = new File();
    file.other("notifications.rate_window", text("5m"));
    const shown = file.shown();
    file.before.push(() => {
      file.other("notifications.rate_window", undefined);
    });
    const got = await change(file, shown, "notifications.rate_window", undefined);
    expect(got.kept).toBe(false);
    expect(valueAt(got.read, "notifications.rate_window")).toBeUndefined();
  });

  it("gives up after a few tries, each lost to another writer", async () => {
    const file = new File();
    for (let i = 0; i < changeTries; i++) {
      file.before.push(() => {
        file.other("codes.symbols", text(String(i)));
      });
    }
    await expect(change(file, file.shown(), "display.deleted.keep", flag(true))).rejects.toThrow("[aborted]");
    expect(file.writes).toBe(0);
    expect(file.reads).toBe(changeTries - 1);
  });

  it("is not tried again after a refusal other than another writer's", async () => {
    const file = new File();
    file.write = () => Promise.reject(new Error("ConnectError: [invalid_argument] codes: require_digit is on"));
    await expect(change(file, file.shown(), "display.deleted.keep", flag(true))).rejects.toThrow("invalid_argument");
    expect(file.reads).toBe(0);
  });
});

describe("what the page sends and shows", () => {
  const of = (path: string) => listed.find((s) => s.path === path) ?? setting({});
  const bounded = setting({ kind: ConfigSettingKind.NUMBER, least: 1n, most: 5n });

  it("sends a typed entry as kithd takes it, and says why another is not one", () => {
    expect(typed(bounded, " 3 ")).toEqual(number(3n));
    expect(typed(bounded, "")).toBeUndefined();
    expect(typed(bounded, "6")).toBe("6 is not from 1 to 5");
    expect(typed(bounded, "0")).toBe("0 is not from 1 to 5");
    expect(typed(bounded, "2.5")).toBe("2.5 is not a whole number");
    const window = of("notifications.rate_window");
    for (const ok of ["90s", "10m", "1h30m", "1.5h", "0", "250ms"]) {
      expect(typed(window, ok), ok).toEqual(text(ok));
    }
    for (const bad of ["soon", "-1m", "10", "1 h", "0x"]) {
      expect(typed(window, bad), bad).toMatch(/not a length of time/);
    }
    expect(typed(of("codes.symbols"), "-_")).toEqual(text("-_"));
  });

  it("says a value in words", () => {
    expect(
      [flag(true), flag(false), number(4n), text(""), text("2m"), list([]), list(["a", "b"]), undefined].map(words),
    ).toEqual(["on", "off", "4", "none", "2m", "none", "a, b", "not set"]);
  });

  it("lists the settings under the TUI's groups in its order, one of a group it does not know last", () => {
    const stray = setting({ path: "x.y", group: "elsewhere" });
    expect(grouped([...listed, stray]).map((g) => [g.title, g.settings.map((s) => s.path)])).toEqual([
      ["Notifications", ["notifications.max_per_room", "notifications.rate_window"]],
      ["Messages", ["display.deleted.keep", "display.unread", "display.tracked.words"]],
      ["Codes and clipboard", ["codes.symbols"]],
      ["Other", ["x.y"]],
    ]);
  });
});

// A value a generated writer gives each setting it changes; none clears it.
const values: Record<string, fc.Arbitrary<ConfigValue | undefined>> = {
  "display.deleted.keep": fc.option(fc.boolean().map(flag), { nil: undefined }),
  "notifications.max_per_room": fc.option(fc.constantFrom(0n, 3n, 9n).map(number), { nil: undefined }),
  "display.unread": fc.option(fc.constantFrom("messages", "notifications").map(text), { nil: undefined }),
  "notifications.rate_window": fc.option(fc.constantFrom("2m", "5m").map(text), { nil: undefined }),
  "codes.symbols": fc.option(fc.constantFrom("-", "_").map(text), { nil: undefined }),
  "display.tracked.words": fc.option(fc.constantFrom(["a"], ["a", "b"], ["b", "a"]).map(list), { nil: undefined }),
};
const paths = Object.keys(values);
const aChange = fc
  .constantFrom(...paths)
  .chain((path) => (values[path] ?? fc.constant(undefined)).map((value) => ({ path, value })));

describe("the settings page and every other writer of config.toml", () => {
  it("never loses another writer's change, nor sets a setting it was not shown", async () => {
    await fc.assert(
      fc.asyncProperty(
        aChange,
        // Before each set of the page's: nothing, or other writers' changes: the TUI's
        // whole-file write (several keys at once), a hand edit, another window's.
        fc.array(fc.array(aChange, { maxLength: 3 }), { maxLength: changeTries + 1 }),
        async (mine, others) => {
          const file = new File();
          const shown = file.shown();
          const theirs = new Map<string, ConfigValue | undefined>();
          for (const changes of others) {
            file.before.push(() => {
              for (const c of changes) {
                file.other(c.path, c.value);
                theirs.set(c.path, c.value);
              }
            });
          }
          let got: Changed | undefined;
          try {
            got = await change(file, shown, mine.path, mine.value);
          } catch {
            // Given up, only with each try lost to another writer: the page's change is
            // not set, every other writer's is.
            expect(others.slice(0, changeTries).filter((c) => c.length > 0)).toHaveLength(changeTries);
            expect(file.writes).toBe(0);
          }
          const now = file.shown();
          for (const path of paths) {
            const want = theirs.has(path) ? theirs.get(path) : valueAt(shown, path);
            if (path !== mine.path) {
              expect(same(valueAt(now, path), want), path).toBe(true);
            }
          }
          if (got === undefined) {
            return;
          }
          const changedElsewhere = theirs.has(mine.path) && !same(theirs.get(mine.path), valueAt(shown, mine.path));
          if (changedElsewhere) {
            expect(same(valueAt(now, mine.path), theirs.get(mine.path))).toBe(true);
            expect(got.kept).toBe(!same(valueAt(now, mine.path), mine.value));
          } else {
            expect(same(valueAt(now, mine.path), mine.value)).toBe(true);
            expect(got.kept).toBe(false);
          }
          // What the page shows is the file as it is.
          for (const path of paths) {
            expect(same(valueAt(got.read, path), valueAt(now, path)), path).toBe(true);
          }
          expect(got.read.revision).toBe(now.revision);
        },
      ),
      { numRuns: 500 },
    );
  });
});
