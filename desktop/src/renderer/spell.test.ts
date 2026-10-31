import fc from "fast-check";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { Misspelling } from "../gen/backend/v1/services_pb";
import {
  autocorrection,
  carried,
  drawn,
  endsWord,
  type Mark,
  marksOf,
  oneCertainEdit,
  pieces,
  replaced,
  shiftMarks,
  type SpellSettings,
  Speller,
  spellDebounce,
  walkAction,
} from "./spell";
import { checked } from "./testing/kithd";

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
});

const settings: SpellSettings = {
  underline: "curly",
  rareUnderline: "dotted",
  autocorrect: "off",
  checkBeforeSend: false,
  rareBeforeSend: false,
};

function mark(text: string, word: string, init: Partial<Mark> = {}): Mark {
  const start = text.indexOf(word);
  return { word, start, end: start + word.length, suggestions: [], rare: false, certain: false, ...init };
}

describe("spelling", () => {
  it("reads kithd's byte offsets as the window's UTF-16 indices, past emoji and Hebrew", () => {
    const text = "😀 שלום wrld 👍🏽 teh";
    const marks = marksOf(
      text,
      checked(
        text,
        new Map([
          ["wrld", ["world"]],
          ["teh", ["the"]],
          ["שלום", []],
        ]),
      ),
    );
    expect(marks.map((m) => text.slice(m.start, m.end))).toEqual(["שלום", "wrld", "teh"]);
  });

  it("moves marks across an edit, dropping the ones it touched and one no longer a whole word", () => {
    const old = "helo wrld teh";
    const marks = [mark(old, "helo"), mark(old, "wrld"), mark(old, "teh")];
    // Typed before all three: each moves.
    expect(shiftMarks(marks, old, `so ${old}`).map((m) => m.start)).toEqual([3, 8, 13]);
    // The middle word edited: it goes until checked again, the others stay true.
    const now = "helo world teh";
    expect(shiftMarks(marks, old, now).map((m) => now.slice(m.start, m.end))).toEqual(["helo", "teh"]);
    // A letter after the last mark's word makes it a part of another word.
    expect(shiftMarks(marks, old, `${old}e`).map((m) => m.word)).toEqual(["helo", "wrld"]);
  });

  it("draws no mark on the word the caret is in, nor of a kind whose underline is none", () => {
    const text = "helo wrld";
    const marks = [mark(text, "helo"), mark(text, "wrld", { rare: true })];
    expect(drawn(marks, 4, settings).map((m) => m.word)).toEqual(["wrld"]);
    expect(drawn(marks, 0, { ...settings, rareUnderline: "none" })).toEqual([]);
    expect(drawn(marks, 0, { ...settings, underline: "none" }).map((m) => m.word)).toEqual(["wrld"]);
    expect(pieces(text, marks, 0, settings)).toEqual([
      { text: "helo ", mark: undefined },
      { text: "wrld", mark: 1 },
    ]);
  });

  it("corrects unasked only a fix that is certain, as the TUI does", () => {
    for (const [word, want, certain] of [
      ["teh", "the", true], // two letters swapped
      ["helo", "hello", true], // one dropped
      ["english", "English", true], // the case
      ["cet", "cat", false], // one wrong: cat, cot, set?
      ["helllo", "hello", false], // one extra
      ["the", "the", false],
    ] as const) {
      expect(oneCertainEdit(word, want), `${word} → ${want}`).toBe(certain);
    }
    const m = (init: Partial<Mark>): Mark => ({
      word: "cet",
      start: 0,
      end: 3,
      suggestions: [],
      rare: false,
      certain: false,
      ...init,
    });
    expect(autocorrection(m({ suggestions: ["cat"] }), "misspellings")).toBe("cat"); // the only one
    expect(autocorrection(m({ suggestions: ["cat", "cot"] }), "all")).toBeUndefined();
    expect(autocorrection(m({ suggestions: ["cat"] }), "rare")).toBeUndefined();
    expect(autocorrection(m({ suggestions: ["cat"], rare: true, certain: true }), "rare")).toBe("cat");
    expect(autocorrection(m({ suggestions: ["cat"], rare: true }), "all")).toBeUndefined();
    expect(autocorrection(m({}), "all")).toBeUndefined(); // usually a name
    expect(endsWord(" ")).toBe(true);
    expect(endsWord("'")).toBe(false);
    expect(endsWord("ש")).toBe(false);
  });

  it("puts right the walk's word and its later copies, not an earlier one skipped, the caret after the last", () => {
    const text = "teh cat teh dog teh";
    const at = (n: number) => {
      let i = -1;
      for (let k = 0; k <= n; k++) i = text.indexOf("teh", i + 1);
      return { word: "teh", start: i, end: i + 3, suggestions: ["the"], rare: false, certain: false };
    };
    const r = replaced(text, { items: [at(0), at(1), at(2)], at: 1, sendWhenDone: false }, "the");
    expect(r.text).toBe("teh cat the dog the");
    expect(r.caret).toBe(19);
    expect(r.items).toHaveLength(2);
    // The word edited meanwhile: nothing is replaced.
    expect(replaced("ten cat", { items: [at(0)], at: 0, sendWhenDone: false }, "the").caret).toBe(-1);
  });

  it("keeps the caret where it was in the words when another writer changes them", () => {
    expect(carried("teh cat", "the cat", 7)).toBe(7);
    expect(carried("hi", "hi\n\nmore", 1)).toBe(1);
    expect(carried("teh cat", "the cat", 0)).toBe(0);
    expect(carried("abcd", "aXd", 2)).toBe(2); // inside the edit: its end
  });

  it("gives the walk the TUI's keys, and keeps typing from reaching the draft", () => {
    expect(walkAction("3", false)).toEqual({ kind: "take", n: 2 });
    expect(walkAction("a", false)).toEqual({ kind: "add" });
    expect(walkAction(" ", false)).toEqual({ kind: "skip" });
    expect(walkAction("S", false)).toEqual({ kind: "skipAll" });
    expect(walkAction("Escape", false)).toEqual({ kind: "stop" });
    expect(walkAction("q", false)).toEqual({ kind: "swallow" });
    expect(walkAction("ArrowLeft", false)).toBeUndefined();
    expect(walkAction("a", true)).toBeUndefined();
  });

  it("stops asking once kithd has nothing to check with", async () => {
    const check = vi.fn(() =>
      Promise.reject(new Error("ConnectError: [failed_precondition] nothing to check spelling with")),
    );
    const s = new Speller(
      check,
      () => undefined,
      () => undefined,
      () => "off",
    );
    s.follow("helo");
    await vi.advanceTimersByTimeAsync(spellDebounce);
    s.follow("helo there");
    await vi.advanceTimersByTimeAsync(spellDebounce);
    expect(check).toHaveBeenCalledTimes(1);
    expect(s.off).toBe(true);
  });
});

/** A step of the walk: the person typing, another writer, kithd answering, its dictionary changing. */
type Step =
  | { kind: "insert"; at: number; what: string }
  | { kind: "delete"; at: number; n: number }
  | { kind: "assistant" }
  | { kind: "deliver"; i: number }
  | { kind: "pause" }
  | { kind: "learn"; word: string }
  | { kind: "unlearn"; word: string }
  | { kind: "reset" };

const words = ["teh", "wrld", "xx", "ab"];
const step: fc.Arbitrary<Step> = fc.oneof(
  {
    weight: 8,
    arbitrary: fc.record({
      kind: fc.constant("insert" as const),
      at: fc.nat(40),
      what: fc.constantFrom("teh", "wrld", "xx", "ab", "e", " ", "\n", "😀", "ש", "'"),
    }),
  },
  {
    weight: 3,
    arbitrary: fc.record({ kind: fc.constant("delete" as const), at: fc.nat(40), n: fc.integer({ min: 1, max: 4 }) }),
  },
  { weight: 2, arbitrary: fc.constant({ kind: "assistant" as const }) },
  { weight: 6, arbitrary: fc.record({ kind: fc.constant("deliver" as const), i: fc.nat(6) }) },
  { weight: 3, arbitrary: fc.constant({ kind: "pause" as const }) },
  { weight: 1, arbitrary: fc.record({ kind: fc.constant("learn" as const), word: fc.constantFrom(...words) }) },
  { weight: 1, arbitrary: fc.record({ kind: fc.constant("unlearn" as const), word: fc.constantFrom(...words) }) },
  { weight: 1, arbitrary: fc.constant({ kind: "reset" as const }) },
);

/** The engine's verdicts at the start: teh and ab each have one fix (autocorrect takes it). */
const dictionary = (): Map<string, string[]> =>
  new Map([
    ["teh", ["the"]],
    ["wrld", ["world", "weld"]],
    ["xx", ["x", "xi"]],
    ["ab", ["abc"]],
  ]);

/** whole is at, moved off the middle of a character: a caret is never there. */
function whole(text: string, at: number): number {
  const i = Math.min(at, text.length);
  const c = text.charCodeAt(i - 1);
  return c >= 0xd800 && c <= 0xdbff ? i - 1 : i;
}

/** onItsWord: a mark covers its word, whole, in text. */
function onItsWord(m: Mark, text: string): boolean {
  return (
    text.slice(m.start, m.end) === m.word &&
    (m.start === 0 || endsWord(text.slice(m.start - 1, m.start))) &&
    (m.end === text.length || endsWord(text.slice(m.end, m.end + 1)))
  );
}

describe("the speller", () => {
  // The draft changes while checks are in flight: typed (a word finished, so autocorrect
  // asks at once), cut, the assistant's words folded in by kithd, the room changed and
  // back. kithd answers late and in any order, its dictionary changing meanwhile (a word
  // learned or installed). Every mark drawn lies on its word in the draft as it is, every
  // correction is made where its word still is, and at rest the marks are kithd's answer
  // for the draft as it ends.
  it("marks only the draft as it is, whatever was answered when", async () => {
    await fc.assert(
      fc.asyncProperty(fc.array(step, { minLength: 1, maxLength: 40 }), async (steps) => {
        let text = "";
        const dict = dictionary();
        const pending: { text: string; answer: (found: Misspelling[]) => void }[] = [];
        const fixes: string[] = [];
        const misplaced: string[] = [];
        const s = new Speller(
          (asked) =>
            new Promise<Misspelling[]>((resolve) => {
              pending.push({ text: asked, answer: resolve });
            }),
          () => undefined,
          (m, put) => {
            // The Composer's fix: made only where the word still is (it checks too).
            // (Called in the check's answer, so a failure is kept for the step to report.)
            fixes.push(`${m.word}@${String(m.start)} in ${JSON.stringify(text)}`);
            if (!onItsWord(m, text)) {
              misplaced.push(fixes.at(-1) ?? "");
              return;
            }
            text = text.slice(0, m.start) + put + text.slice(m.end);
            s.follow(text);
          },
          () => "all",
        );
        const deliver = (i: number) => {
          const p = pending.splice(i % pending.length, 1)[0];
          p?.answer(checked(p.text, dict)); // answered as the dictionary is when it answers
        };
        for (const st of steps) {
          switch (st.kind) {
            case "insert": {
              const at = whole(text, st.at);
              text = text.slice(0, at) + st.what + text.slice(at);
              // One character typed after a word finishes it, as the Composer tells.
              if (Array.from(st.what).length === 1 && endsWord(st.what) && at > 0) {
                s.wordEnded(at);
              }
              s.follow(text);
              break;
            }
            case "delete": {
              const at = whole(text, st.at);
              text = text.slice(0, at) + text.slice(whole(text, at + st.n));
              s.follow(text);
              break;
            }
            case "assistant":
              text = text === "" ? "teh report" : `${text}\n\nteh report`;
              s.follow(text);
              break;
            case "deliver":
              if (pending.length > 0) {
                deliver(st.i);
              }
              break;
            case "pause":
              await vi.advanceTimersByTimeAsync(spellDebounce);
              break;
            case "learn":
              dict.delete(st.word);
              break;
            case "unlearn":
              dict.set(st.word, dictionary().get(st.word) ?? []);
              break;
            case "reset":
              s.reset();
              s.follow(text);
              break;
          }
          await vi.advanceTimersByTimeAsync(0);
          expect(misplaced, `corrected where the word no longer is, after ${st.kind}`).toEqual([]);
          for (const m of s.shown) {
            expect(
              onItsWord(m, text),
              `${m.word} at ${String(m.start)} in ${JSON.stringify(text)} after ${st.kind}`,
            ).toBe(true);
          }
        }
        // At rest: one more keystroke, and every answer comes.
        text = `${text}.`;
        s.follow(text);
        for (let round = 0; round < 20 && (pending.length > 0 || round < 2); round++) {
          await vi.advanceTimersByTimeAsync(spellDebounce);
          while (pending.length > 0) {
            deliver(pending.length - 1); // the newest first: the older ones arrive last
          }
          await vi.advanceTimersByTimeAsync(0);
        }
        // Kept as the TUI keeps them: a word a quote or a hyphen runs on into is not drawn.
        const want = text.trim() === "" ? [] : marksOf(text, checked(text, dict)).filter((m) => onItsWord(m, text));
        expect(
          s.shown.map((m) => `${m.word}@${String(m.start)}`),
          `${JSON.stringify(text)} ${fixes.join("; ")}`,
        ).toEqual(want.map((m) => `${m.word}@${String(m.start)}`));
      }),
      { numRuns: 400 },
    );
  });
});
