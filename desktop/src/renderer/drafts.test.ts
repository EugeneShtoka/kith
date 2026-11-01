import fc from "fast-check";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { BackendService } from "../gen/backend/v1/services_pb";
import { blank, type Composition, Drafts, saveDelay } from "./drafts";
import { call } from "./kith";
import { endsWord, type Speller as SpellerType, Speller, spellDebounce } from "./spell";
import { Kithd, room } from "./testing/kithd";

let kithd: Kithd;

beforeEach(() => {
  vi.useFakeTimers();
  kithd = new Kithd();
  kithd.profiles = ["Solo"];
  vi.stubGlobal("window", { kith: kithd.bridge() });
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

/** settle lets every answer due arrive (the fake answers a turn later). */
async function settle(): Promise<void> {
  for (let i = 0; i < 30; i++) {
    await vi.advanceTimersByTimeAsync(1);
  }
}

/** drive runs p to its end, time passing as it waits. */
async function drive<T>(p: Promise<T>): Promise<T> {
  const state = { done: false };
  const ended = p.finally(() => {
    state.done = true;
  });
  for (let i = 0; i < 1000 && !state.done; i++) {
    await vi.advanceTimersByTimeAsync(10);
  }
  return ended;
}

/** A window's drafts, with the profile chosen as main would have it (Device answered), holding the rooms. */
async function window(changed: () => void = () => undefined): Promise<Drafts> {
  kithd.of("Solo").rooms = rooms.map((r) => room(r));
  await drive(kithd.bridge().call("Device", new Uint8Array()));
  return new Drafts(changed, (err) => {
    if (err !== undefined) console.log("FAILED", err);
  });
}

const rooms = ["!a:x", "!b:x"] as const;

/** A step of the walk: the person, the assistant, kithd's answers and faults, a takeover. */
type Step =
  | { kind: "type" | "send" | "edit" | "assistant" | "between"; room: number }
  | { kind: "pause" | "spell" | "poll" | "takeover" | "close" | "leave" }
  | { kind: "hold"; what: "ReplaceDraft" | "Drafts" | "CheckSpelling" }
  | { kind: "deliver"; i: number }
  | { kind: "fault"; when: "before" | "after" };

const step: fc.Arbitrary<Step> = fc.oneof(
  { weight: 8, arbitrary: fc.record({ kind: fc.constant("type" as const), room: fc.nat(1) }) },
  { weight: 3, arbitrary: fc.record({ kind: fc.constant("send" as const), room: fc.nat(1) }) },
  { weight: 1, arbitrary: fc.record({ kind: fc.constant("edit" as const), room: fc.nat(1) }) },
  { weight: 3, arbitrary: fc.record({ kind: fc.constant("assistant" as const), room: fc.nat(1) }) },
  { weight: 2, arbitrary: fc.record({ kind: fc.constant("between" as const), room: fc.nat(1) }) },
  { weight: 4, arbitrary: fc.constant({ kind: "pause" as const }) },
  // A pause long enough for a spelling check, not for a draft's write.
  { weight: 3, arbitrary: fc.constant({ kind: "spell" as const }) },
  { weight: 3, arbitrary: fc.constant({ kind: "poll" as const }) },
  {
    weight: 3,
    arbitrary: fc.record({
      kind: fc.constant("hold" as const),
      what: fc.constantFrom("ReplaceDraft", "Drafts", "CheckSpelling"),
    }),
  },
  { weight: 5, arbitrary: fc.record({ kind: fc.constant("deliver" as const), i: fc.nat(5) }) },
  {
    weight: 2,
    arbitrary: fc.record({ kind: fc.constantFrom("fault" as const), when: fc.constantFrom("before", "after") }),
  },
  { weight: 1, arbitrary: fc.constant({ kind: "takeover" as const }) },
  { weight: 1, arbitrary: fc.constant({ kind: "close" as const }) },
  { weight: 2, arbitrary: fc.constant({ kind: "leave" as const }) },
);

/** stored is room's draft as kithd holds it, as a composition. */
function stored(room: string): Composition {
  const d = kithd.drafts.get(room);
  return d === undefined
    ? blank
    : {
        body: d.body,
        replyTo: d.replyTo,
        editing: d.editing,
        editSaved: d.editSaved,
        threadRoot: d.threadRoot,
        mentions: [],
      };
}

const shown = (c: Composition) => `${c.body}|${c.editing}|${c.editSaved}|${c.replyTo}`;

describe("drafts", () => {
  // Two writers share every stored draft: this window, its writes rebased in kithd, and
  // kith-mcp appending (also between this window's read and its write). Answers come
  // late and in any order, writes fail before they land or land with the answer lost,
  // and another window takes over (this one saves, then writes no more). At rest every
  // word typed or appended is, exactly once, in a message sent or a stored draft, and
  // the window and kithd agree on every draft.
  it("keep every word once, whatever the interleaving", async () => {
    await fc.assert(
      fc.asyncProperty(fc.array(step, { minLength: 1, maxLength: 40 }), async (steps) => {
        kithd = new Kithd();
        kithd.profiles = ["Solo"];
        vi.stubGlobal("window", { kith: kithd.bridge() });
        // The first room's draft is spell-checked as the composer checks it (spell.ts): its
        // marks follow every change, the assistant's words folded in among them, while
        // checks are held and answered late. Every other word kithd rejects.
        for (let i = 0; i < 200; i += 2) {
          kithd.misspelled.set(`w${String(i)}`, []);
          kithd.misspelled.set(`a${String(i)}`, []);
        }
        const speller: SpellerType = new Speller(
          async (text) => (await call(BackendService.method.checkSpelling, { text })).misspellings,
          () => undefined,
          () => undefined,
          () => "off",
        );
        const spelled = () => {
          speller.follow(drafts.get(rooms[0]).body);
        };
        let drafts = await window(spelled);
        const typed: string[] = [];
        const added: string[] = [];
        const sent: string[] = [];
        // Words typed in each room and not sent: the window never takes one back (a read
        // older than a write, or taken over typing, would).
        const kept = new Map<string, Set<string>>(rooms.map((r) => [r, new Set<string>()]));
        // Words an abrupt close (a crash) may have lost: typed, and not stored when it came;
        // and words sent whose clear had not landed, which may be offered again (as the
        // TUI's model allows of its last write at exit).
        const givenUp = new Set<string>();
        const offeredAgain = new Set<string>();
        let n = 0;
        const append = (room: string) => {
          const word = `a${String(++n)}`;
          if (kithd.append(room, word)) {
            added.push(word);
          }
        };
        for (const s of steps) {
          const room = "room" in s ? rooms[s.room] : "";
          const c = room === undefined ? blank : drafts.get(room);
          switch (s.kind) {
            case "type":
              if (room !== undefined && c.editing === "") {
                const word = `w${String(++n)}`;
                typed.push(word);
                kept.get(room)?.add(word);
                drafts.set(room, { ...c, body: `${c.body} ${word}`.trim() });
              }
              break;
            case "send":
              if (room !== undefined && c.editing === "" && c.body !== "") {
                sent.push(c.body);
                kept.get(room)?.clear();
                drafts.set(room, blank);
              }
              break;
            case "edit":
              if (room !== undefined) {
                drafts.set(
                  room,
                  c.editing === ""
                    ? { ...c, editSaved: c.body, editing: "$mine", body: "fixed" }
                    : { ...c, body: c.editSaved, editing: "", editSaved: "" },
                );
              }
              break;
            case "assistant":
              if (room !== undefined) {
                append(room);
              }
              break;
            case "between":
              if (room !== undefined) {
                kithd.beforeDraft = () => {
                  append(room);
                };
              }
              break;
            case "pause":
              await vi.advanceTimersByTimeAsync(saveDelay);
              break;
            case "spell":
              await vi.advanceTimersByTimeAsync(spellDebounce);
              await settle(); // and the answers not held come
              break;
            case "poll":
              void drafts.load();
              break;
            case "hold":
              // Its answers are held from now (or no longer): they come when delivered.
              if (!kithd.holding.delete(s.what)) {
                kithd.holding.add(s.what);
              }
              break;
            case "deliver":
              kithd.releaseAt(s.i);
              await vi.advanceTimersByTimeAsync(1);
              break;
            case "fault":
              kithd.draftFaults.push(s.when);
              break;
            case "takeover":
              // Another window takes the seat: this one writes what it holds (kithd answering
              // as it steps aside), then nothing more; the new one reads what is stored.
              kithd.holding.clear();
              kithd.draftFaults.length = 0;
              kithd.release();
              await drive(drafts.flush());
              drafts.stop();
              await settle();
              drafts = await window(spelled);
              await drive(drafts.load());
              break;
            case "leave": {
              // The window leaves the profile, kithd failing or slow as it may be: it writes
              // what it holds, a write that failed once more, a little while at most, then
              // nothing more. What is not stored is kept, and put back in the next run's
              // composer when the room is open again (the session's part, done here).
              await drive(drafts.flush());
              const kept = drafts.unstored();
              drafts.stop();
              await settle();
              drafts = await window(spelled);
              kithd.holding.delete("Drafts"); // the next run reads what is stored, its writes still held or failing
              await drive(drafts.load());
              for (const [r, k] of kept) {
                drafts.keep(r, k);
              }
              break;
            }
            case "close": {
              // The window closes at once: nothing more is written, and a write on the wire
              // lands or not. What it typed and kithd does not hold is given up.
              const held = rooms.map((r) => drafts.get(r));
              drafts.stop();
              kithd.holding.clear();
              kithd.draftFaults.length = 0;
              kithd.release();
              await settle();
              const said = sent.flatMap((body) => body.split(/\s+/));
              for (const [i, r] of rooms.entries()) {
                const there = `${stored(r).body} ${stored(r).editSaved}`.split(/\s+/);
                for (const word of there.filter((w) => said.includes(w))) {
                  offeredAgain.add(word);
                }
                for (const word of `${held[i]?.body ?? ""} ${held[i]?.editSaved ?? ""}`.split(/\s+/)) {
                  if (!there.includes(word)) {
                    givenUp.add(word);
                    kept.get(r)?.delete(word);
                  }
                }
              }
              drafts = await window(spelled);
              await drive(drafts.load());
              break;
            }
          }
          spelled();
          const body = drafts.get(rooms[0]).body;
          for (const m of speller.shown) {
            const whole =
              (m.start === 0 || endsWord(body.slice(m.start - 1, m.start))) &&
              (m.end === body.length || endsWord(body.slice(m.end, m.end + 1)));
            expect(body.slice(m.start, m.end) === m.word && whole, `${m.word} marked in ${JSON.stringify(body)}`).toBe(
              true,
            );
          }
          for (const r of rooms) {
            const here = `${drafts.get(r).body} ${drafts.get(r).editSaved}`.split(/\s+/);
            for (const word of kept.get(r) ?? []) {
              expect(here, `${word}, typed in ${r} after ${s.kind}`).toContain(word);
            }
          }
        }
        // At rest: kithd answers everything, and the assistant writes no more.
        kithd.holding.clear();
        kithd.draftFaults.length = 0;
        kithd.beforeDraft = undefined;
        kithd.release();
        let before = "";
        for (let round = 0; round < 12; round++) {
          // A pause, then a poll: as the window's own timers and its poll write and read.
          await vi.advanceTimersByTimeAsync(saveDelay);
          await settle();
          await drive(drafts.load());
          await settle();
          const now = rooms.map((r) => shown(drafts.get(r)) + "/" + shown(stored(r))).join(";");
          if (now === before) {
            break;
          }
          before = now;
        }
        drafts.stop();
        // And the marks are kithd's for the draft as it ends.
        for (let round = 0; round < 3; round++) {
          spelled();
          await vi.advanceTimersByTimeAsync(spellDebounce);
          await settle();
        }
        const end = drafts.get(rooms[0]).body;
        expect(
          speller.shown.map((m) => m.word),
          JSON.stringify(end),
        ).toEqual(end.match(/\b[wa][0-9]*[02468]\b/g) ?? []);

        for (const r of rooms) {
          expect(shown(drafts.get(r)), `${r}: the window and kithd agree`).toBe(shown(stored(r)));
        }
        const count = new Map<string, number>();
        const where = [...sent, ...rooms.flatMap((r) => [stored(r).body, stored(r).editSaved])];
        for (const text of where) {
          for (const word of text.split(/\s+/)) {
            count.set(word, (count.get(word) ?? 0) + 1);
          }
        }
        for (const word of [...typed, ...added]) {
          const times = count.get(word) ?? 0;
          if (!(givenUp.has(word) && times === 0) && !(offeredAgain.has(word) && times === 2)) {
            expect(times, `${word} in ${JSON.stringify(where)}`).toBe(1);
          }
        }
      }),
      { numRuns: 300 },
    );
  });

  it("clear a sent draft at once: a window closed just after sending does not offer it again", async () => {
    let drafts = await window();
    drafts.set("!a:x", { ...blank, body: "hi" });
    await vi.advanceTimersByTimeAsync(saveDelay);
    await settle();
    drafts.set("!a:x", blank); // sent
    await settle();
    drafts.stop(); // and the window is gone
    drafts = await window();
    await drive(drafts.load());
    expect(kithd.drafts.has("!a:x")).toBe(false);
    expect(drafts.get("!a:x")).toEqual(blank);
  });

  it("take no read older than a write: what it read is what the write replaced", async () => {
    const drafts = await window();
    kithd.holding.add("Drafts");
    const read = drafts.load(); // reads the store as it is: no draft
    await settle();
    drafts.set("!a:x", { ...blank, body: "hi" });
    await vi.advanceTimersByTimeAsync(saveDelay);
    await settle();
    expect(kithd.drafts.get("!a:x")?.body).toBe("hi");
    kithd.holding.clear();
    kithd.release(); // the read answers last, with no draft in it
    await drive(read);
    expect(drafts.get("!a:x").body).toBe("hi");
  });

  it("put the assistant's words, written between this window's read and its write, after its own", async () => {
    const drafts = await window();
    kithd.append("!a:x", "the report");
    await drive(drafts.load());
    expect(drafts.get("!a:x").body).toBe("the report");
    drafts.set("!a:x", { ...drafts.get("!a:x"), body: "the report, and hi" });
    kithd.beforeDraft = () => kithd.append("!a:x", "more");
    await vi.advanceTimersByTimeAsync(saveDelay);
    await settle();
    expect(kithd.drafts.get("!a:x")?.body).toBe("the report, and hi\n\nmore");
    expect(drafts.get("!a:x").body).toBe("the report, and hi\n\nmore");
  });

  it("send a write that landed unheard again as this window's own, its words once", async () => {
    const drafts = await window();
    drafts.set("!a:x", { ...blank, body: "hi" });
    kithd.draftFaults.push("after");
    await vi.advanceTimersByTimeAsync(saveDelay);
    await settle();
    kithd.append("!a:x", "a1");
    drafts.set("!a:x", { ...blank, body: "hi there" });
    await vi.advanceTimersByTimeAsync(saveDelay);
    await settle();
    expect(kithd.drafts.get("!a:x")?.body).toBe("hi there\n\na1");
    expect(drafts.get("!a:x").body).toBe("hi there\n\na1");
  });

  it("give two writes in one millisecond stamps apart, so neither is taken for the other", async () => {
    const drafts = await window();
    vi.setSystemTime(5000);
    drafts.set("!a:x", { ...blank, body: "one" });
    await drive(drafts.flush());
    expect(kithd.drafts.get("!a:x")?.updatedUnixMs).toBe(5000n);
    vi.setSystemTime(5000); // the clock reads the same millisecond again
    drafts.set("!a:x", { ...blank, body: "one two" });
    await drive(drafts.flush());
    expect(kithd.drafts.get("!a:x")?.updatedUnixMs).toBe(5001n);
  });
});
