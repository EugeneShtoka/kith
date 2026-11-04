import { describe, expect, it } from "vitest";

import table from "../../../internal/config/testdata/keynames.json";
import { keyName, Keymap, moved } from "./keymap";

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
    expect(k.press("ctrl+k", false)).toEqual(["jump_to"]);
    expect(k.press("g", false)).toEqual([]);
    expect(k.pending).toBe("g");
    expect(k.press("P", false)).toEqual(["profile"]);
    expect(k.pending).toBe("");
  });

  it("ends a sequence a press does not continue, and judges that press alone", () => {
    const k = new Keymap(keys, actions);
    k.press("g", false);
    expect(k.press(",", false)).toEqual(["settings"]);
    k.press("g", false);
    expect(k.press("x", false)).toEqual([]);
    expect(k.pending).toBe("");
  });

  it("lets typing win: only anywhere actions, by presses that type no text, and nothing held", () => {
    const k = new Keymap(keys, actions);
    expect(k.press(",", true)).toEqual([]);
    expect(k.press("g", true)).toEqual([]);
    expect(k.pending).toBe("");
    expect(k.press("ctrl+k", true)).toEqual(["jump_to"]);
    const typed = new Keymap({ jump_to: ["j"] }, actions);
    expect(typed.press("j", true)).toEqual([]); // a letter types itself
    expect(typed.press("j", false)).toEqual(["jump_to"]);
  });

  it("counts the digits before an action, which takes the count; any other press drops it", () => {
    const k = new Keymap({ ...keys, "nav.down": ["j"], "nav.select_newest": ["G"] }, [
      ...actions,
      { path: "nav.down", anywhere: false },
      { path: "nav.select_newest", anywhere: false },
    ]);
    k.press("1", false);
    k.press("2", false);
    expect(k.pending).toBe("12");
    expect([k.press("G", false), k.count]).toEqual([["nav.select_newest"], 12]);
    expect([k.press("j", false), k.count]).toEqual([["nav.down"], 0]); // taken once
    k.press("5", false);
    k.press("g", false);
    expect(k.pending).toBe("5 g");
    expect([k.press("P", false), k.count]).toEqual([["profile"], 5]); // a sequence after a count
    k.press("3", false);
    k.press("q", false); // bound to none of the window's
    expect([k.press("j", false), k.count]).toEqual([["nav.down"], 0]);
    expect(k.press("0", false)).toEqual([]); // no count starts with 0
    expect(k.pending).toBe("");
    k.press("9", false);
    k.press("0", false);
    expect(k.pending).toBe("90");
    k.press("1", true); // typing drops it
    expect(k.pending).toBe("");
  });

  it("lets a sequence bound to a digit win over the count", () => {
    const k = new Keymap({ jump_to: ["2 j"] }, actions);
    k.press("2", false);
    expect([k.press("j", false), k.count]).toEqual([["jump_to"], 0]);
  });

  it("binds only the window's actions, every one bound alike, in priority order", () => {
    const k = new Keymap({ ...keys, settings: [",", "ctrl+k"] }, actions);
    expect(k.press("q", false)).toEqual([]); // quit is the TUI's
    expect(k.press("ctrl+k", false)).toEqual(["jump_to", "settings"]);
    expect(k.press("ctrl+k", true)).toEqual(["jump_to"]); // typing: the anywhere ones
  });
});

describe("a motion", () => {
  // At row 5 of 20, ten of them on screen.
  for (const [path, count, want] of [
    ["nav.down", 0, 6],
    ["nav.down", 3, 8],
    ["nav.down", 99, 19], // held at the end
    ["nav.up", 0, 4],
    ["nav.up", 9, 0],
    ["nav.page_down", 0, 15],
    ["nav.page_up", 0, 0],
    ["nav.half_page_down", 0, 10],
    ["nav.half_page_up", 0, 0],
    ["nav.select_oldest", 0, 0],
    ["nav.select_newest", 0, 19],
    ["nav.scroll_oldest", 0, 0],
    ["nav.scroll_newest", 0, 19],
    ["nav.select_newest", 12, 11], // 12G: the twelfth row
    ["nav.select_oldest", 3, 2],
    ["nav.select_newest", 99, 19],
    ["nav.open", 0, undefined], // no motion
  ] as const) {
    it(`${String(count)} ${path} goes to ${String(want)}`, () => {
      expect(moved(path, count, 5, 10, 20)).toBe(want);
    });
  }
});
