import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import { PersonListedSchema, RailRowSchema } from "../gen/backend/v1/services_pb";
import { choices, hasFieldTerm, matching, personChoices } from "./switcher";
import { room, unread } from "./testing/kithd";

describe("the switcher", () => {
  const rooms = [
    room("!a", { name: "Garden club" }),
    room("!d", { name: "Dana", isDirect: true, members: ["matrix:@dana:example.org"] }),
    room("!b", { name: "Book club" }),
  ];
  const rail = [create(RailRowSchema, { key: "space:!s", label: "Neighbours" })];
  const all = choices(rooms, new Map([["!b", unread("!b", 3)]]), rail);

  it("offers the rooms with something unread first, then the rest, then the places", () => {
    expect(all.map((c) => [c.label, c.detail])).toEqual([
      ["Book club", "3 unread"],
      ["Garden club", ""],
      ["Dana", "DM"],
      ["Neighbours", "place"],
    ]);
  });

  it("keeps the rows that have every word typed, in any case, a DM by its person's ID too", () => {
    expect(matching(all, "CLUB bo").map((c) => c.label)).toEqual(["Book club"]);
    expect(matching(all, "dana:example").map((c) => c.go)).toEqual([{ room: "!d" }]);
    expect(matching(all, "neigh").map((c) => c.go)).toEqual([{ row: 0 }]);
    expect(matching(all, "  ")).toHaveLength(4);
  });

  it("lists people only for a field term, each going to the person on a card kithd listed", () => {
    expect(["role:attorney", "dana role:a x", "x:y"].map(hasFieldTerm)).toEqual([true, true, true]);
    expect(["dana", "role:", ":attorney", "", "  "].map(hasFieldTerm)).toEqual([false, false, false, false, false]);
    const listed = [
      create(PersonListedSchema, { card: "Eli Stone", matched: [{ key: "role", values: ["Family Attorney"] }] }),
    ];
    expect(personChoices(listed)).toEqual([
      {
        key: "person Eli Stone",
        label: "Eli Stone",
        detail: "role: Family Attorney · person",
        match: "",
        go: { person: "Eli Stone" },
      },
    ]);
  });
});
