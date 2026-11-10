import { describe, expect, it } from "vitest";

import { keyName, Keymap } from "./keymap";
import { type Folded, type Layout, reveal, type Target, type Width } from "./panes";

/** visible: target can be seen in layout at width, as styles.css folds the columns. */
function visible(target: Target, l: Layout, width: Width): boolean {
  switch (width) {
    case "wide":
      return target === "rail" ? l.folded === "none" : target === "rooms" ? l.folded !== "rooms" : true;
    case "narrow":
      return target === "rail" ? l.unfolded : true;
    case "narrowest":
      // The unfolded left column lies over the room.
      return target === "rail" || target === "rooms" ? l.unfolded : !l.unfolded;
  }
}

describe("the pane keys", () => {
  const targets: Target[] = ["rail", "rooms", "timeline", "composer"];
  const widths: Width[] = ["wide", "narrow", "narrowest"];
  const foldings: Folded[] = ["none", "places", "rooms"];

  it("show the pane they go to, from every layout, and leave one already shown as it is", () => {
    for (const width of widths) {
      for (const folded of foldings) {
        for (const unfolded of [false, true]) {
          for (const target of targets) {
            const now = { folded, unfolded };
            const after = reveal(target, now, width);
            const where = `${target} from ${JSON.stringify(now)} at ${width}`;
            expect(visible(target, after, width), where).toBe(true);
            if (visible(target, now, width)) {
              expect(after, where).toEqual(now);
            }
          }
        }
      }
    }
  });

  it("unfold the room list alone in a wide window, the rail staying folded", () => {
    expect(reveal("rooms", { folded: "rooms", unfolded: false }, "wide")).toEqual({
      folded: "places",
      unfolded: false,
    });
  });

  it("act from any pane, a field taking typing included, as ctrl+shift with the digit", () => {
    const keys = {
      focus_rail: ["ctrl+shift+1"],
      focus_rooms: ["ctrl+shift+2"],
      focus_timeline: ["ctrl+shift+3"],
      focus_composer: ["ctrl+shift+4"],
    };
    const k = new Keymap(
      keys,
      Object.keys(keys).map((path) => ({ path, anywhere: true })),
    );
    const shifted = ["!", "@", "#", "$"];
    Object.keys(keys).forEach((path, i) => {
      const name = keyName({
        key: shifted[i] ?? "",
        code: `Digit${String(i + 1)}`,
        ctrlKey: true,
        altKey: false,
        shiftKey: true,
        metaKey: false,
      });
      expect(name).toBe(`ctrl+shift+${String(i + 1)}`);
      for (const typing of [false, true]) {
        expect(k.press(name ?? "", typing), `${path} typing=${String(typing)}`).toEqual([path]);
      }
    });
  });
});
