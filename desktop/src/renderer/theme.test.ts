import { create, type MessageInitShape } from "@bufbuild/protobuf";
import fc from "fast-check";
import { describe, expect, it } from "vitest";

import { type ThemeResponse, ThemeResponseSchema, ThemeScope, ThemeSource } from "../gen/device/v1/device_pb";
import {
  changeTheme,
  type ThemeChanged,
  type ThemeRelay,
  themeCSS,
  themeTries,
  modeOf,
  roomColor,
  type Scope,
  senderColor,
} from "./theme";

/**
 * Files is the theme behind the relay: config.toml's [gui.theme] (through kithd) and
 * device.toml's, each written only at the revision it is at (else ABORTED, as main
 * passes it on), each write a new revision of its file alone. before runs as each write
 * reaches it: another writer, between the page's read and its write.
 */
class Files implements ThemeRelay {
  shared = new Map<string, string>();
  device = new Map<string, string>();
  revisions = { [ThemeScope.SHARED]: 1, [ThemeScope.DEVICE]: 1 };
  writes = 0;
  reads = 0;
  readonly before: (() => void)[] = [];

  /** other is another writer setting key in scope's file: the TUI's editor, a window, a hand edit. */
  other(scope: Scope, key: string, value: string): void {
    const file = scope === ThemeScope.SHARED ? this.shared : this.device;
    if (value === "") {
      file.delete(key);
    } else {
      file.set(key, value);
    }
    this.revisions[scope]++;
  }

  theme(): ThemeResponse {
    const values: MessageInitShape<typeof ThemeResponseSchema>["values"] = {};
    for (const key of new Set([...this.shared.keys(), ...this.device.keys()])) {
      const shared = this.shared.get(key) ?? "";
      const device = this.device.get(key) ?? "";
      values[key] = {
        value: device || shared,
        source: device ? ThemeSource.DEVICE : shared ? ThemeSource.SHARED : ThemeSource.UNSPECIFIED,
        shared,
        device,
      };
    }
    return create(ThemeResponseSchema, {
      values,
      configRevision: String(this.revisions[ThemeScope.SHARED]),
      deviceRevision: String(this.revisions[ThemeScope.DEVICE]),
    });
  }

  read(): Promise<ThemeResponse> {
    this.reads++;
    return Promise.resolve(this.theme());
  }

  write(base: ThemeResponse, key: string, scope: Scope, value: string): Promise<ThemeResponse> {
    this.before.shift()?.();
    const at = scope === ThemeScope.SHARED ? base.configRevision : base.deviceRevision;
    if (at !== String(this.revisions[scope])) {
      return Promise.reject(
        new Error("Error invoking remote method 'kith:call': ConnectError: [aborted] config.toml changed since"),
      );
    }
    this.writes++;
    this.other(scope, key, value);
    return this.read();
  }
}

const keys = ["mode", "light.accent", "dark.accent", "people.@a:x", "accounts.whatsapp:main/"];
const scopes: Scope[] = [ThemeScope.SHARED, ThemeScope.DEVICE];
const write = fc.record({
  key: fc.constantFrom(...keys),
  scope: fc.constantFrom(...scopes),
  value: fc.constantFrom("", "#111111", "#222222", "dark"),
});

describe("a change of the theme on the settings page", () => {
  it("gives way to another writer's change of the same key in the same file, and says so", async () => {
    const files = new Files();
    const shown = files.theme();
    files.before.push(() => {
      files.other(ThemeScope.SHARED, "dark.accent", "#222222"); // the TUI's editor
    });
    const got = await changeTheme(files, shown, "dark.accent", ThemeScope.SHARED, "#111111");
    expect(got.kept).toBe(true);
    expect(files.shared.get("dark.accent")).toBe("#222222");
    expect(files.writes).toBe(0);
  });

  it("is written onto this device's file when only the shared one changed: the override stands over it", async () => {
    const files = new Files();
    const shown = files.theme();
    files.other(ThemeScope.SHARED, "dark.accent", "#222222");
    const got = await changeTheme(files, shown, "dark.accent", ThemeScope.DEVICE, "#111111");
    expect(got.kept).toBe(false);
    expect(got.theme.values["dark.accent"]?.value).toBe("#111111");
    // Its reset shows the shared value again.
    const back = await changeTheme(files, got.theme, "dark.accent", ThemeScope.DEVICE, "");
    expect(back.theme.values["dark.accent"]?.value).toBe("#222222");
    expect(back.theme.values["dark.accent"]?.source).toBe(ThemeSource.SHARED);
  });

  it("is not tried again after a failure other than another writer's", async () => {
    const files = new Files();
    files.write = () => Promise.reject(new Error("ConnectError: [invalid_argument] theme.dark.accent: no color"));
    await expect(changeTheme(files, files.theme(), "dark.accent", ThemeScope.SHARED, "x")).rejects.toThrow(
      "invalid_argument",
    );
    expect(files.reads).toBe(0);
  });

  it("never loses another writer's change, nor writes over a key it was not shown", async () => {
    await fc.assert(
      fc.asyncProperty(
        write,
        // Before each write of the page's: none, or another writer's changes, of either file.
        fc.array(fc.array(write, { maxLength: 2 }), { maxLength: themeTries + 1 }),
        async (mine, others) => {
          const files = new Files();
          const shown = files.theme();
          const theirs = new Map<string, string>(); // "scope key" → their value
          for (const changes of others) {
            files.before.push(() => {
              for (const c of changes) {
                files.other(c.scope, c.key, c.value);
                theirs.set(`${String(c.scope)} ${c.key}`, c.value);
              }
            });
          }
          let got: ThemeChanged | undefined;
          try {
            got = await changeTheme(files, shown, mine.key, mine.scope, mine.value);
          } catch {
            // Given up, only with each try lost to a writer of the same file.
            expect(files.writes).toBe(0);
            return;
          }
          const fileOf = (scope: Scope) => (scope === ThemeScope.SHARED ? files.shared : files.device);
          for (const scope of scopes) {
            for (const key of keys) {
              if (scope === mine.scope && key === mine.key) {
                continue;
              }
              expect(fileOf(scope).get(key) ?? "", `${String(scope)} ${key}`).toBe(
                theirs.get(`${String(scope)} ${key}`) ?? "",
              );
            }
          }
          const id = `${String(mine.scope)} ${mine.key}`;
          const changedElsewhere = (theirs.get(id) ?? "") !== "";
          const now = fileOf(mine.scope).get(mine.key) ?? "";
          if (changedElsewhere) {
            expect(now).toBe(theirs.get(id));
            expect(got.kept).toBe(now !== mine.value);
          } else {
            expect(now).toBe(mine.value);
            expect(got.kept).toBe(false);
          }
        },
      ),
      { numRuns: 500 },
    );
  });
});

function theme(init: MessageInitShape<typeof ThemeResponseSchema>): ThemeResponse {
  return create(ThemeResponseSchema, init);
}

describe("the theme drawn", () => {
  const base = theme({
    values: {
      mode: { value: "dark", source: ThemeSource.SHARED, shared: "dark" },
      "dark.accent": { value: "#ABCDEF", source: ThemeSource.DEVICE, device: "#ABCDEF" },
      "light.text": { value: "red; } body { display: none", source: ThemeSource.SHARED },
      "people.@picked:x": { value: "#010101", source: ThemeSource.SHARED },
      "accounts.whatsapp:main/": { value: "#020202", source: ThemeSource.DEVICE },
      "accounts.telegram:t/": { value: "#030303" }, // its network's, not picked
    },
    senderPalette: ["#100000", "#200000"],
    networkColors: { WhatsApp: "#00aa00", Telegram: "#0000aa" },
    identities: {
      "@ada:x": { key: "id:Ada", color: "#3ad900" },
      "@ada:y": { key: "id:Ada", color: "#3ad900" },
      "@bob:x": { key: "id:@bob:x", color: "" },
    },
  });
  const slots = new Map([
    ["id:Ada", 1],
    ["id:@bob:x", 1],
    ["@cy:x", 0],
    ["@far:x", 9],
  ]);

  it("colors a person as picked, else as the TUI does: pinned, else their slot", () => {
    expect(senderColor(base, slots, "@picked:x")).toBe("#010101");
    expect(senderColor(base, slots, "@ada:y")).toBe("#3ad900");
    expect(senderColor(base, slots, "@bob:x")).toBe("#200000"); // their identity's slot
    expect(senderColor(base, slots, "@cy:x")).toBe("#100000");
    expect(senderColor(base, slots, "@far:x")).toBeUndefined(); // past the palette: the window's own
    expect(senderColor(base, slots, "@new:x")).toBeUndefined(); // no slot yet
    expect(senderColor(undefined, slots, "@cy:x")).toBeUndefined();
  });

  it("colors a room's mark by its account, picked, else by its network as the TUI names its rooms", () => {
    expect(roomColor(base, { id: "whatsapp:main/123@s.whatsapp.net", network: "WhatsApp" })).toBe("#020202");
    expect(roomColor(base, { id: "whatsapp:work/123@s.whatsapp.net", network: "WhatsApp" })).toBe("#00aa00");
    expect(roomColor(base, { id: "telegram:t/42", network: "Telegram" })).toBe("#0000aa");
    expect(roomColor(base, { id: "!r:x", network: "" })).toBeUndefined();
  });

  it("sets each mode's roles in its own scheme, puts no value that is no color in it, and has a class for each color", () => {
    const css = themeCSS(base);
    expect(css).toContain("@media (prefers-color-scheme: dark) { :root { --accent: #ABCDEF; } }");
    expect(css).not.toContain("prefers-color-scheme: light");
    expect(css).not.toContain("display: none");
    for (const hex of ["#100000", "#200000", "#00aa00", "#3ad900", "#010101", "#020202", "#030303"]) {
      expect(css).toContain(`.c-${hex.slice(1)} { --hue: ${hex}; }`);
      expect(css).toContain(`.net.c-${hex.slice(1)} { color: ${hex}; }`);
    }
    expect(themeCSS(undefined)).toBe("");
  });

  it("follows the system unless the mode says light or dark", () => {
    expect(modeOf(base)).toBe("dark");
    expect(modeOf(undefined)).toBe("system");
    expect(modeOf(theme({ values: { mode: { value: "light" } } }))).toBe("light");
    expect(modeOf(theme({}))).toBe("system");
  });
});
