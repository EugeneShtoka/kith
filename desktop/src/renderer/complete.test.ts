import { create } from "@bufbuild/protobuf";
import fc from "fast-check";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { MessageSchema } from "../gen/backend/v1/types_pb";
import {
  Assist,
  assistDebounce,
  type Candidate,
  caretInRTL,
  type CompleteSettings,
  dominant,
  firstWord,
  type ModelAnswer,
  offered,
  Phrases,
  recaseLike,
  takePhrase,
  takeWord,
  trailingWord,
  wordBeingTyped,
} from "./complete";

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
});

const on: CompleteSettings = { complete: true, ghost: true, ratio: 3, model: true };
const message = (body: string, sender = "@them:x") => create(MessageSchema, { id: body, body, sender });

describe("completion", () => {
  it("finishes the word at the draft's end, three letters in, with what saves two more", () => {
    expect(trailingWord("see you tomor")).toBe("tomor");
    expect(trailingWord("it's")).toBe("s");
    expect(wordBeingTyped("see you to", 10)).toBeUndefined(); // two letters
    expect(wordBeingTyped("see you tomor", 5)).toBeUndefined(); // the caret not at the end
    const cs: Candidate[] = [
      { word: "tomorrow", score: 30 },
      { word: "tomb", score: 9 }, // saves too little
      { word: "today", score: 1 }, // does not go on from it
    ];
    expect(offered(cs, "Tom").map((c) => c.word)).toEqual(["Tomorrow"]);
    expect(recaseLike("TOM", "tomorrow")).toBe("TOMORROW");
    expect(dominant(offered(cs, "tom"), 3)?.word).toBe("tomorrow");
    expect(
      dominant(
        [
          { word: "a", score: 5 },
          { word: "b", score: 2 },
        ],
        3,
      ),
    ).toBeUndefined();
  });

  it("takes a word with a space after it, and a phrase, at the draft's end", () => {
    expect(takeWord("see you tomor", "tomorrow")).toEqual({ text: "see you tomorrow ", caret: 17 });
    expect(takeWord("see you tomor", "today")).toBeUndefined();
    expect(takePhrase("see you at ", "the office")).toEqual({ text: "see you at the office ", caret: 22 });
    expect(takePhrase("see you at ", "noon.")?.text).toBe("see you at noon.");
    expect(firstWord("the office")).toBe("the ");
    expect(caretInRTL("שלום ", 5)).toBe(true);
    expect(caretInRTL("hi שלום ok", 10)).toBe(false);
  });

  it("knows what this room says next, its own words weighing more", () => {
    const rows = [
      message("see you at the office"),
      message("see you at the office"),
      message("see you at noon", "@me:x"),
    ];
    const p = Phrases.of(rows, new Set(["@me:x"]));
    expect(p.next("you", "at", 1)).toBe("noon"); // 3 against 2
    expect(p.next("you", "at", 3)).toBeUndefined(); // not by 3 times
    expect(p.nextWords("we meet at ", 2)).toEqual(["noon", "the"]);
  });

  it("draws the leader of the word being typed, and the next words at a word's end", async () => {
    const vocabulary: Candidate[] = [{ word: "tomorrow", score: 30 }];
    const a = new Assist(
      {
        complete: (prefix) => Promise.resolve(vocabulary.filter((c) => c.word.startsWith(prefix))),
        model: () =>
          Promise.resolve({ suggestion: "the office", options: ["the office", "noon"], refusal: "", model: "m" }),
      },
      () => undefined,
    );
    const rows = Phrases.of([message("see you at noon"), message("see you at noon")], new Set());
    a.follow("see you tomor", 13, on, false);
    await vi.advanceTimersByTimeAsync(assistDebounce);
    expect(a.offer("see you tomor", on, () => rows)).toMatchObject({ ghost: "row", from: "word", lead: "tomorrow" });
    expect(a.offer("see you tomor", { ...on, ghost: false }, () => rows)).toMatchObject({
      ghost: "",
      lead: "tomorrow",
    });
    a.follow("see you at ", 11, on, false);
    await vi.advanceTimersByTimeAsync(0);
    expect(a.offer("see you at ", on, () => rows)).toMatchObject({
      ghost: "the office",
      from: "model",
      options: ["the office", "noon"],
      marker: "m",
    });
  });
});

/** A step: a letter typed or taken back, a word ended, an answer of kithd's arriving. */
type Step = { kind: "type"; ch: string } | { kind: "back" } | { kind: "word" | "model"; i: number } | { kind: "pause" };

const step: fc.Arbitrary<Step> = fc.oneof(
  {
    weight: 6,
    arbitrary: fc.record({
      kind: fc.constant("type" as const),
      ch: fc.constantFrom("tom ", "tom", "mee", "t", "o", "e", " "),
    }),
  },
  { weight: 2, arbitrary: fc.constant({ kind: "back" as const }) },
  { weight: 4, arbitrary: fc.record({ kind: fc.constantFrom("word" as const, "model" as const), i: fc.nat(5) }) },
  { weight: 3, arbitrary: fc.constant({ kind: "pause" as const }) },
);

const vocabulary: Candidate[] = [
  { word: "tomorrow", score: 40 },
  { word: "tomato", score: 12 },
  { word: "tomtom", score: 3 },
  { word: "meeting", score: 20 },
  { word: "meet", score: 2 },
  { word: "meetings", score: 5 },
];
/** The question a draft asks the model: trailing blanks aside, as a held space asks once. */
const asked = (draft: string) => draft.replace(/[ \t]+$/, "").length;
/** kithd's answer: the two best words going on from prefix (an earlier prefix's may miss one). */
const answer = (prefix: string) => vocabulary.filter((c) => c.word.startsWith(prefix)).slice(0, 2);

describe("the assist", () => {
  // Words are typed and taken back while kithd's answers come late and in any order:
  // what is offered is always about the word as now written, and at rest it is kithd's
  // answer for that word, never an earlier one's; a word taken goes where the word is.
  it("offers only what answers the draft as it is", async () => {
    await fc.assert(
      fc.asyncProperty(fc.array(step, { minLength: 1, maxLength: 40 }), async (steps) => {
        let text = "";
        const pending: { prefix: string; resolve: (c: Candidate[]) => void }[] = [];
        const models: { draft: string; resolve: (a: ModelAnswer) => void }[] = [];
        const a = new Assist(
          {
            complete: (prefix) =>
              new Promise((resolve) => {
                pending.push({ prefix, resolve });
              }),
            model: (draft) =>
              new Promise((resolve) => {
                models.push({ draft, resolve });
              }),
          },
          () => undefined,
        );
        const word = (i: number) => {
          const p = pending.splice(i % Math.max(pending.length, 1), 1)[0];
          p?.resolve(answer(p.prefix));
        };
        const model = (i: number) => {
          const m = models.splice(i % Math.max(models.length, 1), 1)[0];
          m?.resolve({ suggestion: `after${String(asked(m.draft))}`, options: [], refusal: "", model: "m" });
        };
        for (const st of steps) {
          if (st.kind === "type") {
            text += st.ch;
          } else if (st.kind === "back") {
            text = text.slice(0, -1);
          } else if (st.kind === "word") {
            word(st.i);
          } else if (st.kind === "model") {
            model(st.i);
          } else {
            await vi.advanceTimersByTimeAsync(assistDebounce);
          }
          a.follow(text, text.length, on, false);
          await vi.advanceTimersByTimeAsync(0);
          const o = a.offer(text, on, () => new Phrases());
          const typed = wordBeingTyped(text, text.length);
          for (const w of o.words) {
            expect(typed !== undefined && w.word.startsWith(typed), `${w.word} for ${JSON.stringify(text)}`).toBe(true);
          }
          if (o.from === "word") {
            expect(takeWord(text, o.lead)?.text.startsWith(text.slice(0, text.length - (typed ?? "").length))).toBe(
              true,
            );
          }
          if (o.from === "model") {
            expect(o.ghost).toBe(`after${String(asked(text))}`); // the model's answer for this draft
          }
        }
        // At rest every answer comes, the oldest last.
        for (let round = 0; round < 3; round++) {
          await vi.advanceTimersByTimeAsync(assistDebounce);
          while (pending.length > 0 || models.length > 0) {
            word(pending.length - 1);
            model(models.length - 1);
          }
          await vi.advanceTimersByTimeAsync(0);
        }
        const typed = wordBeingTyped(text, text.length);
        const want = typed === undefined ? [] : offered(answer(typed.toLowerCase()), typed);
        const o = a.offer(text, on, () => new Phrases());
        expect(o.words, JSON.stringify(text)).toEqual(want);
        expect(o.lead).toBe(dominant(want, on.ratio)?.word ?? "");
        if (o.from === "model") {
          expect(o.ghost).toBe(`after${String(asked(text))}`);
        }
      }),
      { numRuns: 400 },
    );
  });
});
