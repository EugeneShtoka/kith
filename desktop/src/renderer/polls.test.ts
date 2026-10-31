import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import { PollVotersResponseSchema } from "../gen/backend/v1/services_pb";
import { type Poll, PollHiding, PollSchema, type PollVoter, PollVoterSchema } from "../gen/backend/v1/types_pb";
import pollsTable from "../../../internal/domain/testdata/polls.json";
import {
  asked,
  canTakeBack,
  choice,
  chosenBy,
  pollNote,
  pollTally,
  tick,
  ticked,
  voterLines,
  voterName,
  type VotersRead,
} from "./polls";

function poll(init: MessageInitShape<typeof PollSchema>): Poll {
  return create(PollSchema, { question: "Lunch?", ...init });
}

const lunch = (mine: string[] = [], multiple = false) =>
  poll({
    multiple,
    options: [
      { id: "s", text: "Soup", votes: 3n, mine: mine.includes("s") },
      { id: "p", text: "Pasta", votes: 0n, mine: mine.includes("p") },
      { id: "t", text: "Tacos", votes: 2n, mine: mine.includes("t") },
    ],
  });

describe("a poll's tally", () => {
  // The same table internal/domain's conformance test holds to Poll.Tally.
  for (const c of pollsTable.cases) {
    it(`is Go's: ${c.name}`, () => {
      const p = poll({ options: c.votes.map((n, i) => ({ id: String(i), votes: BigInt(n) })) });
      expect(p.options.map((o) => pollTally(p, o))).toEqual(c.want);
    });
  }

  it("says when the poll is closed, or takes several answers", () => {
    expect(pollNote(poll({}))).toBe("");
    expect(pollNote(poll({ multiple: true }))).toBe("choose any");
    expect(pollNote(poll({ multiple: true, closed: true }))).toBe("closed");
  });
});

describe("a vote", () => {
  it("of one answer may be taken back while the poll is open", () => {
    expect(canTakeBack(lunch())).toBe(false);
    expect(canTakeBack(lunch(["s"]))).toBe(true);
    expect(canTakeBack({ ...lunch(["s"]), closed: true })).toBe(false);
    expect(canTakeBack(lunch(["s"], true))).toBe(false); // unticking every answer does
  });

  it("of several starts from this person's answers, and names those ticked in the poll's order", () => {
    const p = lunch(["t"], true);
    let ticks = ticked(p, undefined);
    expect([...ticks]).toEqual(["t"]);
    ticks = tick(tick(ticks, "s"), "p");
    expect(choice(p, ticks)).toEqual(["s", "p", "t"]);
    ticks = tick(tick(tick(ticks, "s"), "p"), "t");
    expect(choice(p, ticks)).toEqual([]); // none: the vote taken back
    expect([...ticked(p, ticks)]).toEqual([]);
  });
});

describe("who voted", () => {
  const voter = (id: string, name: string, options: string[]): PollVoter =>
    create(PollVoterSchema, { id, name, options });
  const selves = new Set(["@me:x"]);
  const name = (v: PollVoter) => voterName(v, selves);
  const read = (answer: MessageInitShape<typeof PollVotersResponseSchema>): VotersRead => ({
    room: "!r",
    event: "$p",
    read: true,
    answer: create(PollVotersResponseSchema, answer),
    error: "",
  });

  it("names this person you, first, then the rest as their network calls them, in order", () => {
    const voters = [
      voter("@zed:x", "Zed", ["s"]),
      voter("@me:x", "Me Myself", ["s"]),
      voter("@ann:x", "", ["s"]),
      voter("@bo:x", "Bo", ["t"]),
    ];
    expect(chosenBy(voters, "s", name)).toEqual(["you", "@ann:x", "Zed"]);
    expect(chosenBy(voters, "p", name)).toEqual([]);
  });

  it("lists each answer someone chose, with those counted and not named as more", () => {
    const voters = [voter("@me:x", "", ["s"]), voter("@bo:x", "Bo", ["s", "t"])];
    expect(voterLines(lunch(["s"]), read({ voters }), name)).toEqual(["Soup: you, Bo, 1 more", "Tacos: Bo, 1 more"]);
    expect(voterLines(lunch(), read({}), name)).toEqual(["Soup: 3 more", "Tacos: 2 more"]);
    const none = poll({ options: [{ id: "a", text: "A" }] });
    expect(voterLines(none, read({}), name)).toEqual(["No one has voted yet"]);
  });

  it("says why no one is named, and while it is read, and when it could not be", () => {
    const p = lunch();
    expect(voterLines(p, read({ hidden: PollHiding.ANONYMOUS }), name)).toEqual([
      "An anonymous poll: no one who voted is named",
    ]);
    expect(voterLines(p, read({ hidden: PollHiding.VOTE_FIRST }), name)).toEqual([
      "Who voted is shown once you have voted",
    ]);
    expect(voterLines(p, { ...read({}), read: false }, name)).toEqual(["Reading who voted…"]);
    expect(voterLines(p, { ...read({}), error: "offline" }, name)).toEqual(["Could not read who voted: offline"]);
  });
});

describe("a poll asked", () => {
  it("needs a question and two answers; blank answers are rows not used", () => {
    expect(asked(" Lunch? ", [" Soup ", "", "Tacos"], true)).toEqual({
      question: "Lunch?",
      options: ["Soup", "Tacos"],
      multiple: true,
    });
    expect(asked("", ["Soup", "Tacos"], false)).toBeUndefined();
    expect(asked("Lunch?", ["Soup", " "], false)).toBeUndefined();
  });
});
