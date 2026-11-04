import { describe, expect, it } from "vitest";

import golden from "../../../internal/config/testdata/default_keys.golden?raw";
import { filtered, type HelpSection, helpSections, keysOf } from "./help";

describe("the help", () => {
  const sections: HelpSection[] = [
    { title: "Anywhere", rows: [{ paths: ["jump_to"], what: "Go to a room" }] },
    {
      title: "On a message",
      rows: [
        { paths: ["timeline.reply"], what: "Reply to it" },
        { paths: ["timeline.next_day", "timeline.prev_day"], what: "The next day" },
      ],
    },
  ];
  const keys = { jump_to: ["ctrl+k"], "timeline.next_day": ["]"], "timeline.prev_day": ["["] };

  it("names only [keys] actions", () => {
    const paths = new Set(golden.split("\n").map((line) => line.split(" = ")[0]));
    expect(helpSections.flatMap((s) => s.rows.flatMap((r) => r.paths)).filter((p) => !paths.has(p))).toEqual([]);
  });

  it("draws a row's keys as bound now, every path's in turn", () => {
    expect(sections[1]?.rows.map((r) => keysOf(r, keys))).toEqual(["(unbound)", "], ["]);
  });

  it("keeps a section whose title matches whole, else its matching rows, else drops it", () => {
    expect(filtered(sections, keys, "")).toEqual(sections);
    expect(filtered(sections, keys, " MESSAGE ")).toEqual([sections[1]]);
    expect(filtered(sections, keys, "reply").map((s) => s.rows.map((r) => r.what))).toEqual([["Reply to it"]]);
    expect(filtered(sections, keys, "ctrl+k").map((s) => s.title)).toEqual(["Anywhere"]); // by its keys
    expect(filtered(sections, keys, "unbound").map((s) => s.title)).toEqual(["On a message"]);
    expect(filtered(sections, keys, "nothing")).toEqual([]);
  });
});
