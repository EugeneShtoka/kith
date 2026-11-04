import { describe, expect, it } from "vitest";

import table from "../../../internal/config/testdata/keynames.json";
import { keyName, Keymap } from "./keymap";

describe("a press's name", () => {
  for (const c of table.cases) {
    it(`is ${c.name} for ${JSON.stringify(c)}`, () => {
      const press = {
        key: c.key,
        code: c.code,
        ctrlKey: "ctrl" in c,
        altKey: "alt" in c,
        shiftKey: "shift" in c,
        metaKey: "meta" in c,
      };
      expect(keyName(press)).toBe(c.name);
    });
  }

  it("is none for a modifier alone", () => {
    expect(
      keyName({ key: "Control", code: "ControlLeft", ctrlKey: true, altKey: false, shiftKey: false, metaKey: false }),
    ).toBe(undefined);
  });
});

describe("the keymap", () => {
  const keys = { jump_to: ["ctrl+k", "g j"], settings: [","], profile: ["g P"], quit: ["q"] };
  const actions = [
    { path: "jump_to", anywhere: true },
    { path: "settings", anywhere: false },
    { path: "profile", anywhere: false },
  ];

  it("acts on a single press, and on a sequence once it completes, holding it until then", () => {
    const k = new Keymap(keys, actions);
    expect(k.press("ctrl+k", false)).toBe("jump_to");
    expect(k.press("g", false)).toBe(undefined);
    expect(k.pending).toBe("g");
    expect(k.press("P", false)).toBe("profile");
    expect(k.pending).toBe("");
  });

  it("ends a sequence a press does not continue, and judges that press alone", () => {
    const k = new Keymap(keys, actions);
    k.press("g", false);
    expect(k.press(",", false)).toBe("settings");
    k.press("g", false);
    expect(k.press("x", false)).toBe(undefined);
    expect(k.pending).toBe("");
  });

  it("lets typing win: only anywhere actions, by presses that type no text, and nothing held", () => {
    const k = new Keymap(keys, actions);
    expect(k.press(",", true)).toBe(undefined);
    expect(k.press("g", true)).toBe(undefined);
    expect(k.pending).toBe("");
    expect(k.press("ctrl+k", true)).toBe("jump_to");
    const typed = new Keymap({ jump_to: ["j"] }, actions);
    expect(typed.press("j", true)).toBe(undefined); // a letter types itself
    expect(typed.press("j", false)).toBe("jump_to");
  });

  it("binds only the window's actions, the first of two bound alike", () => {
    const k = new Keymap({ ...keys, settings: [",", "ctrl+k"] }, actions);
    expect(k.press("q", false)).toBe(undefined); // quit is the TUI's
    expect(k.press("ctrl+k", false)).toBe("jump_to");
  });
});
