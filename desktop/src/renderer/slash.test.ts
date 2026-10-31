import { describe, expect, it } from "vitest";

import table from "../../../internal/tui/testdata/commands.json";
import { builtins, offered, pollOf, typed } from "./slash";

interface Case {
  input: string;
  message?: string;
  builtin?: string;
  user?: string;
  arg?: string;
}

// The same table internal/tui's conformance test holds to slashCommands and submit.
describe("the composer's commands, as the TUI's", () => {
  it("are the TUI's, in its order", () => {
    expect(builtins.map((c) => c.name)).toEqual(table.names);
  });
  for (const c of table.cases as Case[]) {
    it(`read ${JSON.stringify(c.input)}`, () => {
      const got = typed(c.input);
      const shown =
        got.kind === "message"
          ? { message: got.body }
          : got.kind === "builtin"
            ? { builtin: got.command.name, arg: got.arg }
            : { user: got.name, arg: got.arg };
      const want =
        c.message !== undefined
          ? { message: c.message }
          : c.user !== undefined
            ? { user: c.user, arg: c.arg }
            : { builtin: c.builtin, arg: c.arg };
      expect(shown).toEqual(want);
    });
  }
});

describe("the / list", () => {
  const users = [
    { name: "zoom", applies: true, places: [] },
    { name: "standup", applies: false, places: ["space:Work"] },
    { name: "me", applies: true, places: [] }, // a built-in's name: the built-in wins
  ];

  it("offers the commands a slash word at the start begins, the window's own saying which it has not got", () => {
    const rows = offered("/s", 2, users) ?? [];
    expect(rows.map((r) => r.name)).toEqual(["/summary", "/search", "/starred", "/scheduled", "/shortcut"]);
    expect(rows.find((r) => r.name === "/summary")?.detail).toMatch(/not in the window yet/);
    expect(rows.find((r) => r.name === "/scheduled")?.detail).not.toMatch(/not in the window/);
  });

  it("offers the commands folder's that run in the room, after the TUI's", () => {
    const names = (offered("/", 1, users) ?? []).map((r) => r.name);
    expect(names.at(-1)).toBe("/zoom");
    expect(names).not.toContain("/standup");
    expect(names.filter((n) => n === "/me")).toHaveLength(1);
  });

  it("is offered only for one slash word at the very start", () => {
    expect(offered("/me waves", 9, users)).toBeUndefined();
    expect(offered("//me", 4, users)).toBeUndefined();
    expect(offered("hi /me", 6, users)).toBeUndefined();
    expect(offered("/me", 3, users)?.map((r) => r.insert)).toEqual(["/me ", "/mentions"]);
  });
});

describe("a poll asked by /poll", () => {
  it("reads as the TUI's parsePollArg", () => {
    expect(pollOf("Lunch? | pizza | sushi")).toEqual({ question: "Lunch?", options: ["pizza", "sushi"], multiple: false });
    expect(pollOf("Lunch? | pizza | sushi | ANY")).toEqual({
      question: "Lunch?",
      options: ["pizza", "sushi"],
      multiple: true,
    });
    expect(pollOf("Lunch? | pizza")).toBeUndefined();
    expect(pollOf("Lunch? | pizza | | sushi")).toBeUndefined();
    expect(pollOf(" | pizza | sushi")).toBeUndefined();
  });
});
