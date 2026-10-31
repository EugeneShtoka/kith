import { clone, create } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";
import fc from "fast-check";
import { describe, expect, it } from "vitest";

import {
  type Message,
  MessageSchema,
  PollSchema,
  ReactionSchema,
  ReactionUpdateSchema,
} from "../gen/backend/v1/types_pb";
import { compare } from "./merge";
import { message, row } from "./testing/rows";
import reactionsTable from "../../../internal/domain/testdata/reactions.json";
import { type Ask, newest, tally, type Ticket, Timeline } from "./timeline";

const room = "!r";

/**
 * A model of kithd's cache for one room (and writes to another), its Messages stream
 * and its pages, against which a Timeline is driven step by step: the cache written,
 * frames delivered late or lost to a read_again, pages read at any time after they were
 * asked and landing any time after that.
 */
class Cache {
  readonly rows = new Map<string, Message>();
  /** The frames written, not delivered yet, in the order the cache wrote them. */
  queue: Message[] = [];
  private made = 0;
  private clock = 0;

  constructor(initial: number) {
    for (let i = 0; i < initial; i++) {
      this.put(this.fresh(i * 10), false);
    }
  }

  ordered(): Message[] {
    return [...this.rows.values()].sort(compare);
  }

  private fresh(ts: number, inRoom = room): Message {
    this.made++;
    return message({ id: `$${String(this.made)}`, ts: ts + 1, body: `m${String(this.made)}` }, inRoom);
  }

  /** put writes m, sending it on the stream (a change) when told to. */
  private put(m: Message, send = true): void {
    if (m.roomId === room) {
      this.rows.set(m.id, m);
    }
    if (send) {
      this.queue.push(clone(MessageSchema, m));
    }
  }

  private pick(i: number): Message | undefined {
    const all = this.ordered();
    return all.length === 0 ? undefined : all[i % all.length];
  }

  /** write is one write to the cache, chosen by kind and i. */
  write(kind: number, i: number): void {
    this.clock++;
    // A poll is voted in again and again, then ended: those writes go mostly to a message
    // asking one already.
    const polls = this.ordered().filter((x) => x.poll !== undefined);
    const m = (kind === 7 || (kind === 6 && i % 3 !== 0)) && polls.length > 0 ? polls[i % polls.length] : this.pick(i);
    switch (kind) {
      case 0: // a new message, now
        this.put(this.fresh(1000 + this.clock * 10));
        return;
      case 1: // history backfilled: an old message
        this.put(this.fresh(i % 400));
        return;
      case 2: // a message in another room
        this.put(this.fresh(1000 + this.clock, "!other"));
        return;
    }
    if (m === undefined) {
      return;
    }
    const next = clone(MessageSchema, m);
    switch (kind) {
      case 3: // edited
        if (next.redacted) return;
        next.edited = true;
        next.body = `${m.body}+`;
        next.editedAtUnixMs = BigInt(10_000 + this.clock);
        next.revisionId = `$e${String(this.clock)}`;
        break;
      case 4: // its edit deleted: back to the first words
        if (next.redacted || !next.edited) return;
        next.edited = false;
        next.body = m.body.replace(/\++$/, "");
        next.editedAtUnixMs = 0n;
        next.revisionId = "";
        break;
      case 5: // deleted
        next.redacted = true;
        next.body = "";
        next.editedAtUnixMs = 0n;
        next.revisionId = "";
        break;
      case 6: // votes, one after another: each changes its poll's counts, up or down, and this person's answers
        for (const k of [i, i + 1]) {
          next.poll = create(PollSchema, {
            question: m.poll?.question ?? "Lunch?",
            closed: m.poll?.closed ?? false,
            options: [
              { id: "s", text: "Soup", votes: BigInt(k % 3), mine: k % 2 === 0 },
              { id: "t", text: "Tacos", votes: BigInt(k % 5), mine: k % 4 === 1 },
            ],
          });
          this.put(clone(MessageSchema, next));
        }
        return;
      case 7: // its poll ended, or its question edited, its votes kept
        if (next.poll === undefined || next.redacted) return;
        if (i % 2 === 0) {
          next.poll.closed = true;
        } else {
          next.poll.question = `${next.poll.question}+`;
          next.edited = true;
          next.editedAtUnixMs = BigInt(10_000 + this.clock);
          next.revisionId = `$e${String(this.clock)}`;
        }
        break;
      default: // moved in time (a network's order corrected)
        next.timestamp = timestampFromMs(1 + (i % 1200));
    }
    this.put(next);
  }

  /** page is what kithd answers ask with, read now. */
  page(ask: Ask): { rows: Message[]; next: string } {
    const all = this.ordered();
    const size = 4;
    switch (ask.kind) {
      case "latest":
        return { rows: all.slice(-size), next: "" };
      case "network":
      case "cached": {
        const before = ask.kind === "network" ? ask.from : ask.before;
        const at = before === "" ? all.length : all.findIndex((m) => m.id === before);
        const from = Math.max(0, at - size);
        // The cached page holds its anchor too, as MessagesAround does.
        const rows = all.slice(from, ask.kind === "cached" ? at + 1 : at);
        const next = ask.kind === "network" && from > 0 ? (all[from]?.id ?? "") : "";
        return { rows, next };
      }
    }
  }
}

/** The steps a run interleaves. */
type Step =
  | { kind: "write"; what: number; i: number }
  | { kind: "deliver"; n: number }
  | { kind: "ask" }
  | { kind: "read" }
  | { kind: "land" }
  | { kind: "fail" }
  | { kind: "older" }
  | { kind: "readAgain" }
  | { kind: "seek"; i: number; cached: boolean };

const step: fc.Arbitrary<Step> = fc.oneof(
  { weight: 6, arbitrary: fc.record({ kind: fc.constant("write"), what: fc.nat(8), i: fc.nat(10_000) }) },
  { weight: 4, arbitrary: fc.record({ kind: fc.constant("deliver"), n: fc.integer({ min: 1, max: 4 }) }) },
  { weight: 3, arbitrary: fc.constant({ kind: "ask" }) },
  { weight: 3, arbitrary: fc.constant({ kind: "read" }) },
  { weight: 3, arbitrary: fc.constant({ kind: "land" }) },
  { weight: 1, arbitrary: fc.constant({ kind: "fail" }) },
  { weight: 2, arbitrary: fc.constant({ kind: "older" }) },
  { weight: 1, arbitrary: fc.constant({ kind: "readAgain" }) },
  {
    weight: 1,
    arbitrary: fc.record({ kind: fc.constant("seek"), i: fc.nat(10_000), cached: fc.boolean() }),
  },
);

/**
 * pollStep is a step in a room already shown: votes, a poll ended or edited, among new
 * messages, edits and deletions, against pages and frames, lost frames included. No
 * page fails: a failed page stops the reading until the window asks again, whatever
 * the rows hold.
 */
const pollStep: fc.Arbitrary<Step> = fc.oneof(
  {
    weight: 6,
    arbitrary: fc.record({ kind: fc.constant("write"), what: fc.constantFrom(0, 3, 5, 6, 6, 7), i: fc.nat(10_000) }),
  },
  { weight: 4, arbitrary: fc.record({ kind: fc.constant("deliver"), n: fc.integer({ min: 1, max: 4 }) }) },
  { weight: 3, arbitrary: fc.constant({ kind: "ask" }) },
  { weight: 3, arbitrary: fc.constant({ kind: "read" }) },
  { weight: 3, arbitrary: fc.constant({ kind: "land" }) },
  { weight: 2, arbitrary: fc.constant({ kind: "older" }) },
  { weight: 1, arbitrary: fc.constant({ kind: "readAgain" }) },
);

/** Run is one window over one cache: the page in flight, asked and maybe read. */
class Run {
  readonly cache: Cache;
  readonly t = new Timeline(room, "");
  private inFlight: { ticket: Ticket; read?: { rows: Message[]; next: string } } | undefined;
  /** Every ticket's answer, kept to land again late: one asked before a reload. */
  private stale: { ticket: Ticket; read: { rows: Message[]; next: string } }[] = [];
  /** The message the last jump was to. */
  sought: string | undefined;

  constructor(initial: number) {
    this.cache = new Cache(initial);
  }

  do(s: Step): void {
    switch (s.kind) {
      case "write":
        this.cache.write(s.what, s.i);
        break;
      case "deliver":
        for (const m of this.cache.queue.splice(0, s.n)) {
          this.t.frame(m);
        }
        break;
      case "ask":
        if (this.inFlight === undefined) {
          const ticket = this.t.ask();
          this.inFlight = ticket && { ticket };
        }
        break;
      case "read":
        if (this.inFlight !== undefined) {
          this.inFlight.read ??= this.cache.page(this.inFlight.ticket.ask);
        }
        break;
      case "land": {
        const f = this.inFlight;
        if (f?.read !== undefined) {
          this.inFlight = undefined;
          this.t.land(f.ticket, f.read.rows, f.read.next);
        } else {
          // An answer from before a reload, arriving now.
          const old = this.stale.shift();
          if (old !== undefined) {
            this.t.land(old.ticket, old.read.rows, old.read.next);
          }
        }
        break;
      }
      case "fail":
        if (this.inFlight !== undefined) {
          this.t.failed(this.inFlight.ticket);
          this.inFlight = undefined;
        }
        break;
      case "older":
        this.t.older();
        break;
      case "seek": {
        // A jump to a message the cache holds (a search hit), or to one it does not.
        const all = this.cache.ordered();
        const to = s.cached && all.length > 0 ? all[s.i % all.length] : undefined;
        this.sought = to?.id ?? `$gone${String(s.i)}`;
        this.t.seek(this.sought);
        break;
      }
      case "readAgain":
        // The stream could not resume: every frame not delivered is lost, and kithd
        // says to read again. An answer in flight still arrives, later.
        this.cache.queue = [];
        if (this.inFlight !== undefined) {
          const f = this.inFlight;
          this.stale.push({ ticket: f.ticket, read: f.read ?? this.cache.page(f.ticket.ask) });
          this.inFlight = undefined;
        }
        this.t.reload();
        break;
    }
  }

  /** waiting: a page is in flight. */
  private waiting(): boolean {
    return this.inFlight !== undefined;
  }

  /** settle delivers every frame and answers every page due, asking for no older one. */
  settle(): void {
    if (!this.t.loaded) {
      this.t.older(); // a first page that failed is asked again when the window asks
    }
    for (let i = 0; i < 1000; i++) {
      this.do({ kind: "deliver", n: this.cache.queue.length });
      if (!this.waiting()) {
        this.do({ kind: "ask" });
        if (!this.waiting()) {
          return;
        }
      }
      this.do({ kind: "read" });
      this.do({ kind: "land" });
    }
    throw new Error("the timeline kept asking");
  }
}

/** drawn is a row as the table writes it, and its poll whole: every answer's count and mark, and whether it ended. */
const drawn = (m: Message) => ({ ...row(m), poll: m.poll });

/** shownMatchesCache: what is shown is the cache's newest messages, in its order, as it holds them. */
function shownMatchesCache(run: Run): void {
  const shown = run.t.view().rows.map(drawn);
  const cache = run.cache.ordered().map(drawn);
  expect(shown).toEqual(cache.slice(cache.length - shown.length));
  if (cache.length > 0) {
    expect(shown.length).toBeGreaterThan(0);
  }
}

describe("the timeline over any interleaving of pages and frames", () => {
  it("shows the cache's newest messages in its order, each as the cache holds it", () => {
    fc.assert(
      fc.property(fc.nat(12), fc.array(step, { maxLength: 80 }), (initial, steps) => {
        const run = new Run(initial);
        for (const s of steps) {
          run.do(s);
        }
        run.settle();
        shownMatchesCache(run);
      }),
      { numRuns: 400 },
    );
  });

  it("finds a jump's message wherever the cache holds it, reading back to it, and says when it holds none", () => {
    fc.assert(
      fc.property(
        fc.nat(12),
        fc.array(step, { maxLength: 80 }),
        fc.nat(10_000),
        fc.boolean(),
        (initial, steps, i, cached) => {
          const run = new Run(initial);
          for (const s of [...steps, { kind: "seek", i, cached } as const]) {
            run.do(s);
          }
          run.settle();
          shownMatchesCache(run);
          const sought = run.sought ?? "";
          const held = run.cache.ordered().some((m) => m.id === sought);
          expect(run.t.view().target).toBe(held ? sought : "");
          expect(run.t.view().rows.some((m) => m.id === run.t.view().target)).toBe(held);
          // Held, it may still have been said missing: written after the last page was
          // read, it came as a frame once the pages ran out.
          if (!held) {
            expect(run.t.jumpMissed()).toBe(true);
          }
        },
      ),
      { numRuns: 2000 },
    );
  });

  it("keeps each poll shown as the cache holds it, its votes, end and edits coming before and after pages", () => {
    fc.assert(
      fc.property(fc.nat(12), fc.array(pollStep, { maxLength: 80 }), (initial, steps) => {
        const run = new Run(initial + 1);
        run.settle(); // shown: what changes now changes rows on screen
        for (const s of steps) {
          run.do(s);
        }
        run.settle();
        shownMatchesCache(run);
      }),
      { numRuns: 400 },
    );
  });

  it("keeps a page read before a frame from taking the frame's change back", () => {
    const run = new Run(3);
    run.do({ kind: "ask" });
    run.do({ kind: "read" }); // the cache as it was
    run.do({ kind: "write", what: 3, i: 2 }); // the newest edited
    run.do({ kind: "deliver", n: 1 });
    run.do({ kind: "land" });
    shownMatchesCache(run);
  });

  it("leaves out an edit of a message older than every row, and shows it once a page reaches it", () => {
    const run = new Run(12);
    run.settle();
    const before = run.t.view().rows.map((m) => m.id);
    run.do({ kind: "write", what: 3, i: 0 }); // the oldest message edited
    run.do({ kind: "deliver", n: 1 });
    expect(run.t.view().rows.map((m) => m.id)).toEqual(before);
    shownMatchesCache(run);
    for (let i = 0; i < 6; i++) {
      run.do({ kind: "older" });
      run.settle();
    }
    expect(run.t.view().start).toBe(true);
    shownMatchesCache(run);
  });

  it("ignores a frame for a room not open", () => {
    const run = new Run(3);
    run.settle();
    run.do({ kind: "write", what: 2, i: 0 });
    run.do({ kind: "deliver", n: 1 });
    expect(run.t.view().rows).toHaveLength(3);
    shownMatchesCache(run);
  });

  it("reads again as far back as it was scrolled when told to, and keeps those rows shown meanwhile", () => {
    const run = new Run(30);
    run.settle();
    for (let i = 0; i < 3; i++) {
      run.do({ kind: "older" });
      run.settle();
    }
    const reached = run.t.view().rows.length;
    expect(reached).toBeGreaterThan(8);
    // Changes the stream loses, all through the stretch shown.
    for (let i = 0; i < reached; i += 3) {
      run.do({ kind: "write", what: 3, i: 30 - reached + i });
    }
    run.do({ kind: "readAgain" });
    run.do({ kind: "ask" });
    expect(run.t.view().rows).toHaveLength(reached); // still on screen while read again
    run.settle();
    expect(run.t.view().rows).toHaveLength(reached);
    shownMatchesCache(run);
  });

  it("drops an answer asked for before a reload", () => {
    const run = new Run(10);
    run.settle(); // the newest four shown
    run.do({ kind: "older" });
    run.do({ kind: "ask" });
    run.do({ kind: "read" }); // the four before them, as they were
    run.do({ kind: "write", what: 3, i: 3 }); // one of those edited
    run.do({ kind: "readAgain" }); // and its frame lost
    run.do({ kind: "land" }); // the answer read before
    run.settle();
    shownMatchesCache(run);
  });

  it("forgets the frames from before a reload: what was lost after them would be taken back", () => {
    const run = new Run(3);
    run.settle();
    run.do({ kind: "older" });
    run.do({ kind: "ask" }); // a page in flight, the frames meanwhile held
    run.do({ kind: "write", what: 3, i: 2 });
    run.do({ kind: "deliver", n: 1 });
    run.do({ kind: "write", what: 3, i: 2 }); // edited again
    run.do({ kind: "readAgain" }); // and that frame lost
    run.settle();
    shownMatchesCache(run);
  });
});

describe("a reply's quote", () => {
  const target = message({ id: "$t", ts: 5, body: "the question" });
  const reply = message({ id: "$a", ts: 100, body: "an answer", replyTo: "$t" });

  it("is fetched when its message is not a row, and changes as its message does", () => {
    const t = new Timeline(room, "");
    const ticket = t.ask();
    expect(ticket?.ask).toEqual({ kind: "latest" });
    t.land(ticket as Ticket, [reply]);
    expect(t.unquoted(6)).toEqual(["$t"]);
    expect(t.unquoted(6)).toEqual([]); // asked once
    t.fetchedQuote(target);
    expect(t.quoted("$t")?.body).toBe("the question");

    const deleted = message({ id: "$t", ts: 5, redacted: true, redactedBy: "@mod" });
    t.frame(deleted); // outside the rows, yet the quote follows it
    expect(t.view().rows.map((m) => m.id)).toEqual(["$a"]);
    expect(t.quoted("$t")?.redacted).toBe(true);
  });

  it("keeps a deletion that came while it was fetched over the fetched copy", () => {
    const t = new Timeline(room, "");
    t.land(t.ask() as Ticket, [reply]);
    t.unquoted(6);
    t.frame(message({ id: "$t", ts: 5, redacted: true }));
    t.fetchedQuote(target); // read before the deletion
    expect(t.quoted("$t")?.redacted).toBe(true);
  });
});

describe("reactions", () => {
  // The same table internal/domain's conformance test holds to AggregateReactions.
  for (const c of reactionsTable.cases) {
    it(`tally as Go's: ${c.name}`, () => {
      const rs = c.reactions.map((x) => create(ReactionSchema, { ...x, roomId: room, target: "$a" }));
      expect(tally(rs)).toEqual(c.want);
    });
  }

  const r = (id: string, key: string) => create(ReactionSchema, { id, roomId: room, target: "$a", key });

  it("are each counted once, and go when taken back", () => {
    const t = new Timeline(room, "");
    t.reactionsRead([r("$1", "👍"), r("$1", "👍"), r("$2", "🎉")]);
    t.reacted(create(ReactionUpdateSchema, { reaction: r("$2", "🎉") }));
    t.reacted(create(ReactionUpdateSchema, { reaction: r("$3", "👍") }));
    t.reacted(create(ReactionUpdateSchema, { reaction: r("$1", "👍"), removed: true }));
    t.reacted(create(ReactionUpdateSchema, { reaction: { ...r("$9", "x"), roomId: "!other" } }));
    expect(
      t
        .view()
        .reactions.get("$a")
        ?.map((x) => x.id),
    ).toEqual(["$2", "$3"]);
  });
});

describe("the newest message a receipt is for", () => {
  it("is the main timeline's, not a thread's", () => {
    const rows = [message({ id: "$a", ts: 1 }), message({ id: "$b", ts: 2, threadRoot: "$a" })];
    expect(newest(rows)?.id).toBe("$a");
  });
});
