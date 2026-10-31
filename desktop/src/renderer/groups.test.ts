import { describe, expect, it } from "vitest";

import { continues, dayLabel, groupGap, newDay, sameDay } from "./groups";
import { message } from "./testing/rows";

const at = (y: number, mo: number, d: number, h = 12, mi = 0) => new Date(y, mo, d, h, mi).getTime();

describe("groups", () => {
  it("put a day divider above the first row shown and where the day changes", () => {
    const a = message({ id: "a", ts: at(2026, 9, 30, 23, 59), sender: "@ann:x" });
    const b = message({ id: "b", ts: at(2026, 9, 31, 0, 1), sender: "@ann:x" });
    const c = message({ id: "c", ts: at(2026, 9, 31, 9), sender: "@bob:x" });
    expect(newDay(undefined, a)).toBe(true);
    expect(newDay(a, b)).toBe(true);
    expect(newDay(b, c)).toBe(false);
    // A message with no time starts no day, and one after it does.
    const timeless = message({ id: "t", sender: "@ann:x" });
    expect(newDay(c, timeless)).toBe(false);
    expect(newDay(timeless, c)).toBe(true);
  });

  it("group one sender's run within the gap, the same day, with nothing drawn between", () => {
    const ann = (id: string, ts: number) => message({ id, ts, sender: "@ann:x" });
    const nine = at(2026, 9, 31, 9);
    const a = ann("a", nine);
    expect(continues(undefined, a, false)).toBe(false);
    expect(continues(a, ann("b", nine + groupGap), false)).toBe(true);
    expect(continues(a, ann("c", nine + groupGap + 1), false)).toBe(false);
    expect(continues(a, message({ id: "d", ts: nine + 60_000, sender: "@bob:x" }), false)).toBe(false);
    expect(continues(a, ann("e", nine + 60_000), true)).toBe(false);
    // Across midnight a divider stands between them, so the group starts again.
    const late = ann("f", at(2026, 9, 30, 23, 58));
    expect(continues(late, ann("g", at(2026, 9, 31, 0, 1)), false)).toBe(false);
    // Out of order, or with no time or no sender: drawn whole.
    expect(continues(a, ann("h", nine - 60_000), false)).toBe(false);
    expect(continues(a, message({ id: "i", sender: "@ann:x" }), false)).toBe(false);
    const nobody = message({ id: "j", ts: nine });
    expect(continues(nobody, message({ id: "k", ts: nine + 60_000 }), false)).toBe(false);
  });

  it("name a divider's day Today, Yesterday, or by its date", () => {
    const now = new Date(2026, 9, 31, 10);
    expect(dayLabel(new Date(2026, 9, 31, 0, 0), now)).toBe("Today");
    expect(dayLabel(new Date(2026, 9, 30, 23, 59), now)).toBe("Yesterday");
    const older = dayLabel(new Date(2026, 9, 29), now);
    expect(older).not.toMatch(/Today|Yesterday/);
    expect(older).not.toMatch(/2026/);
    expect(dayLabel(new Date(2025, 9, 29), now)).toMatch(/2025/);
    // Yesterday across a month's turn.
    expect(dayLabel(new Date(2026, 9, 31), new Date(2026, 10, 1, 8))).toBe("Yesterday");
    expect(sameDay(new Date(2026, 0, 1), new Date(2025, 0, 1))).toBe(false);
  });
});
