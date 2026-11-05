import { create } from "@bufbuild/protobuf";
import fc from "fast-check";
import { describe, expect, it } from "vitest";

import { FileRoomResponseSchema, FilingPlaceSchema, SpaceFiledOutcome } from "../gen/backend/v1/services_pb";
import { asked, filingKey, offered, opened, toggled, told } from "./filing";
import type { Press } from "./keymap";

const place = (init: { space?: string; tag?: string; label: string; in?: boolean }) => create(FilingPlaceSchema, init);

describe("the filing picker", () => {
  // Over any places, held or not, and any presses: what is asked of kithd is exactly the
  // places ticked otherwise than they hold the room, each once; a place pressed an even
  // number of times, or never, is not asked about; a busy picker takes no press.
  it("asks only for the places ticked otherwise than they hold the room", () => {
    const arb = fc.record({
      places: fc.uniqueArray(
        fc.record({ space: fc.boolean(), name: fc.constantFrom("A", "B", "C", "D"), in: fc.boolean() }),
        { selector: (p) => `${String(p.space)}${p.name}`, maxLength: 6 },
      ),
      presses: fc.array(fc.nat(8), { maxLength: 12 }),
      busyAt: fc.option(fc.nat(12), { nil: undefined }),
    });
    fc.assert(
      fc.property(arb, ({ places, presses, busyAt }) => {
        const shown = places.map((p) =>
          place(p.space ? { space: `!${p.name}`, label: p.name, in: p.in } : { tag: p.name, label: p.name, in: p.in }),
        );
        let f = offered(opened("!r", "Room"), shown, false);
        const keys = (f.places ?? []).map((p) => p.key);
        const pressed = new Map<string, number>();
        presses.forEach((n, i) => {
          if (i === busyAt) {
            f = { ...f, busy: true };
          }
          const key = keys[n] ?? "nowhere";
          if (!f.busy && keys.includes(key)) {
            pressed.set(key, (pressed.get(key) ?? 0) + 1);
          }
          f = toggled(f, key);
        });
        const ask = asked(f);
        const want = (f.places ?? []).filter((p) => (pressed.get(p.key) ?? 0) % 2 === 1);
        expect(ask.spaces).toEqual(want.filter((p) => p.space !== "").map((p) => ({ spaceId: p.space, in: !p.in })));
        expect(ask.tags).toEqual(want.filter((p) => p.space === "").map((p) => ({ tag: p.tag, in: !p.in })));
      }),
    );
  });

  it("ticks what holds the room, and says when filing_spaces names no space here", () => {
    const f = offered(
      opened("!r", "Room"),
      [place({ tag: "Work", label: "Job", in: true }), place({ tag: "Later", label: "Later" })],
      false,
    );
    expect([...f.ticked]).toEqual(["tag Work"]);
    expect(f.note).toBe("");
    const none = offered(opened("!r", "Room"), [], true);
    expect(none.places).toEqual([]);
    expect(none.note).toMatch(/filing_spaces/);
  });

  it("tells in one line where the room went and what was refused, by the rail's labels", () => {
    const f = offered(
      opened("!r", "Alpha"),
      [
        place({ tag: "Pinned", label: "Pinned" }),
        place({ tag: "Archive", label: "Archived", in: true }),
        place({ space: "!w", label: "Job", in: true }),
        place({ space: "!f", label: "Friends" }),
        place({ space: "!m", label: "Matrix HQ" }),
        place({ space: "!x", label: "Locked" }),
      ],
      false,
    );
    let g = f;
    for (const key of ["tag Pinned", "tag Archive", "space !w", "space !f", "space !m", "space !x"]) {
      g = toggled(g, key);
    }
    const ask = asked(g);
    const r = create(FileRoomResponseSchema, {
      spaces: [
        { spaceId: "!w", outcome: SpaceFiledOutcome.FILED },
        { spaceId: "!f", outcome: SpaceFiledOutcome.FILED },
        { spaceId: "!m", outcome: SpaceFiledOutcome.PARENT_KEPT },
        { spaceId: "!x", outcome: SpaceFiledOutcome.REFUSED, error: "no power" },
      ],
      archiveFailed: "telegram: not now",
    });
    expect(told(g, ask, r)).toBe(
      "Alpha is in Pinned, and out of Archived · filed into Friends · taken out of Job · " +
        "moved in Matrix HQ but kept its own record of where it lives (no power there) · " +
        "refused by Locked (no power) · its network did not archive or unarchive it: telegram: not now",
    );
    expect(told(f, asked(f), create(FileRoomResponseSchema))).toBe("No change.");
  });
});

describe("the filing picker's keys", () => {
  const keys = {
    "nav.down": ["j", "down"],
    "nav.up": ["k", "up"],
    "picker.toggle": ["space"],
    "picker.accept": ["enter", "tab"],
    "picker.close": ["esc"],
  };
  const press = (key: string): Press => ({
    key,
    code: "",
    ctrlKey: false,
    altKey: false,
    shiftKey: false,
    metaKey: false,
  });

  // Every key the picker binds, on the first, a middle and the last of three rows, and off
  // them on a button: the cursor moves and stays inside, and a button keeps its own keys.
  it("moves along the rows, ticks, applies and closes, by the TUI's keys", () => {
    const cases: [string, number, ReturnType<typeof filingKey>][] = [
      ["j", 0, { to: 1 }],
      ["ArrowDown", 1, { to: 2 }],
      ["j", 2, { to: 2 }],
      ["k", 2, { to: 1 }],
      ["ArrowUp", 0, { to: 0 }],
      [" ", 1, "toggle"],
      ["Enter", 1, "apply"],
      ["Tab", 0, "apply"],
      ["Escape", 2, "close"],
      ["Escape", -1, "close"],
      ["j", -1, undefined],
      [" ", -1, undefined],
      ["Enter", -1, undefined],
      ["x", 1, undefined],
    ];
    for (const [key, at, want] of cases) {
      expect(filingKey(press(key), keys, at, 3), `${key} on row ${String(at)}`).toEqual(want);
    }
  });

  // Rebound in [keys], the picker follows: a tick on t, and space no longer ticks.
  it("takes the keys as [keys] binds them", () => {
    const rebound = { ...keys, "picker.toggle": ["t"] };
    expect(filingKey(press("t"), rebound, 0, 3)).toBe("toggle");
    expect(filingKey(press(" "), rebound, 0, 3)).toBeUndefined();
  });
});
