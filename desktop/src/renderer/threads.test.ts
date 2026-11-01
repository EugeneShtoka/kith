import { clone } from "@bufbuild/protobuf";
import fc from "fast-check";
import { describe, expect, it } from "vitest";

import table from "../../../internal/domain/testdata/threads.json";
import { type Message, MessageSchema } from "../gen/backend/v1/types_pb";
import { compare } from "./merge";
import { message, type Row, row } from "./testing/rows";
import { collapse, OpenThread, type ThreadTicket, threadMessages } from "./threads";

interface Case {
  name: string;
  rows: Row[];
  main: string[];
  threads: {
    root: string;
    count: number;
    latest: string;
    latestSender?: string;
    latestSenderName?: string;
    anchor: string;
    rootLoaded: boolean;
  }[];
  threadOf: Record<string, string[]>;
}

// The same table internal/domain's conformance test holds to CollapseThreads and
// ThreadMessages.
describe("threads, as Go groups them", () => {
  const cases = table.cases as unknown as Case[];
  it("has cases", () => {
    expect(cases.length).toBeGreaterThan(0);
  });
  for (const c of cases) {
    it(c.name, () => {
      const msgs = c.rows.map((r) => message(r));
      const { main, threads } = collapse(msgs);
      expect(main.map((m) => m.id)).toEqual(c.main);
      expect(threads).toEqual(c.threads.map((t) => ({ latestSender: "", latestSenderName: "", ...t })));
      for (const [root, want] of Object.entries(c.threadOf)) {
        expect(threadMessages(msgs, root).map((m) => m.id)).toEqual(want);
      }
    });
  }
});

const room = "!r";
const root = "$root";

/**
 * A model of kithd's cache for one room with one thread, its frames and its pages: the
 * root and replies written, edited and deleted while the pane reads the root and pages of
 * replies (ThreadPage, three at a time), each answered any time after it was asked.
 */
class Cache {
  readonly rows = new Map<string, Message>();
  queue: Message[] = [];
  private made = 0;

  constructor(replies: number) {
    this.put(message({ id: root, ts: 5, body: "the root" }, room), false);
    for (let i = 0; i < replies; i++) {
      this.put(this.fresh(true), false);
    }
  }

  ordered(): Message[] {
    return [...this.rows.values()].sort(compare);
  }

  private fresh(inThread: boolean): Message {
    this.made++;
    return message(
      {
        id: `$${String(this.made)}`,
        ts: 10 + this.made * 10,
        body: `m${String(this.made)}`,
        threadRoot: inThread ? root : "",
      },
      room,
    );
  }

  private put(m: Message, send = true): void {
    this.rows.set(m.id, m);
    if (send) {
      this.queue.push(clone(MessageSchema, m));
    }
  }

  write(kind: number, i: number): void {
    const all = this.ordered();
    const m = kind >= 2 ? all[i % all.length] : undefined;
    const next = m === undefined ? undefined : clone(MessageSchema, m);
    switch (kind) {
      case 0: // a reply in the thread
        this.put(this.fresh(true));
        return;
      case 1: // a message in the main timeline
        this.put(this.fresh(false));
        return;
      case 2: // the root, or a message, edited
        if (next === undefined || next.redacted) return;
        next.edited = true;
        next.body = `${next.body}+`;
        next.editedAtUnixMs = BigInt(10_000 + i);
        next.revisionId = `$e${String(i)}`;
        break;
      case 3: // its edit deleted: back to the first words
        if (next === undefined || next.redacted || !next.edited) return;
        next.edited = false;
        next.body = next.body.replace(/\++$/, "");
        next.editedAtUnixMs = 0n;
        next.revisionId = "";
        break;
      default: // deleted: the root most of all
        if (next === undefined) return;
        if (i % 2 === 0) {
          const r = this.rows.get(root);
          if (r === undefined) return;
          Object.assign(next, clone(MessageSchema, r));
        }
        next.redacted = true;
        next.body = "";
        next.editedAtUnixMs = 0n;
        next.revisionId = "";
    }
    this.put(next);
  }

  /** answer is what kithd answers t with, read now: the root, or a page of replies before from. */
  answer(t: ThreadTicket): { rows: Message[]; next: string } {
    const ask = t.ask;
    if (ask.kind === "root") {
      const r = this.rows.get(root);
      return { rows: r === undefined ? [] : [r], next: "" };
    }
    const replies = this.ordered().filter((m) => m.threadRoot === root);
    const before = ask.from === "" ? replies.length : replies.findIndex((m) => m.id === ask.from);
    const from = Math.max(0, before - 3);
    return { rows: replies.slice(from, before), next: from > 0 ? (replies[from]?.id ?? "") : "" };
  }
}

type Step =
  | { kind: "write"; what: number; i: number }
  | { kind: "deliver"; n: number }
  | { kind: "ask" }
  | { kind: "read" }
  | { kind: "land" }
  | { kind: "fail" }
  | { kind: "again" }
  | { kind: "older" }
  | { kind: "readAgain" };

const step: fc.Arbitrary<Step> = fc.oneof(
  { weight: 6, arbitrary: fc.record({ kind: fc.constant("write"), what: fc.nat(4), i: fc.nat(1000) }) },
  { weight: 4, arbitrary: fc.record({ kind: fc.constant("deliver"), n: fc.integer({ min: 1, max: 4 }) }) },
  { weight: 3, arbitrary: fc.constant({ kind: "ask" }) },
  { weight: 3, arbitrary: fc.constant({ kind: "read" }) },
  { weight: 3, arbitrary: fc.constant({ kind: "land" }) },
  { weight: 1, arbitrary: fc.constant({ kind: "fail" }) },
  { weight: 1, arbitrary: fc.constant({ kind: "again" }) },
  { weight: 2, arbitrary: fc.constant({ kind: "older" }) },
  { weight: 1, arbitrary: fc.constant({ kind: "readAgain" }) },
);

/**
 * Run is one thread pane over one cache. The room's timeline is the newest few messages
 * as the window last heard of them (the timeline's own tests hold it to the cache); or,
 * holding, every message from its first rows on, as a Timeline keeps them, read again as
 * far back when told to.
 */
class Run {
  readonly cache: Cache;
  readonly t = new OpenThread(room, root);
  /** The messages as the window has heard of them: what its timeline shows. */
  private readonly heard = new Map<string, Message>();
  /** Holding, the messages before the timeline's first rows: never shown. */
  private readonly before = new Set<string>();
  private inFlight: { ticket: ThreadTicket; read?: { rows: Message[]; next: string } } | undefined;
  private stale: { ticket: ThreadTicket; read: { rows: Message[]; next: string } }[] = [];
  /** A failed read is to be asked again after a wait, not over yet (the window's timer). */
  private later = false;

  constructor(
    replies: number,
    private readonly shown: number,
    private readonly holds = false,
  ) {
    this.cache = new Cache(replies);
    for (const m of this.cache.ordered()) {
      this.heard.set(m.id, m);
    }
    if (holds) {
      for (const m of this.cache.ordered().slice(0, Math.max(0, this.heard.size - shown))) {
        this.before.add(m.id);
      }
    }
  }

  rows(): Message[] {
    const all = [...this.heard.values()].sort(compare);
    return this.holds ? all.filter((m) => !this.before.has(m.id)) : all.slice(this.heard.size - this.shown);
  }

  do(s: Step): void {
    switch (s.kind) {
      case "write":
        this.cache.write(s.what, s.i);
        break;
      case "deliver":
        for (const m of this.cache.queue.splice(0, s.n)) {
          this.heard.set(m.id, m);
          this.t.frame(m);
        }
        break;
      case "ask":
        if (this.inFlight === undefined) {
          const ticket = this.t.ask(this.rows());
          this.inFlight = ticket && { ticket };
        }
        break;
      case "read":
        if (this.inFlight !== undefined) {
          this.inFlight.read ??= this.cache.answer(this.inFlight.ticket);
        }
        break;
      case "land": {
        const f = this.inFlight;
        if (f?.read !== undefined) {
          this.inFlight = undefined;
          this.t.land(f.ticket, f.read.rows, f.read.next);
        } else {
          const old = this.stale.shift();
          if (old !== undefined) {
            this.t.land(old.ticket, old.read.rows, old.read.next);
          }
        }
        break;
      }
      case "fail": {
        // The read in flight refused, or else an answer from before a reload, refused now.
        const ticket = this.inFlight?.ticket ?? this.stale.shift()?.ticket;
        this.inFlight = undefined;
        if (ticket !== undefined && this.t.failed(ticket)) {
          this.later = true;
        }
        break;
      }
      case "again": // the wait after a failed read is over
        if (this.later) {
          this.later = false;
          this.t.again();
        }
        break;
      case "older":
        this.t.older();
        break;
      case "readAgain":
        // Frames lost: kithd says to read again; an answer in flight still arrives, late.
        for (const m of this.cache.queue.splice(0)) {
          this.heard.set(m.id, m); // the timeline reads its stretch again
        }
        if (this.inFlight !== undefined) {
          const f = this.inFlight;
          this.stale.push({ ticket: f.ticket, read: f.read ?? this.cache.answer(f.ticket) });
          this.inFlight = undefined;
        }
        this.t.reload();
        break;
    }
  }

  /** waiting: a read is in flight. */
  private waiting(): boolean {
    return this.inFlight !== undefined;
  }

  /**
   * settle delivers every frame and answers every read, back to the thread's first reply
   * when the window asks for older ones, and waits out every wait: the i-th read it
   * answers fails where fails[i] says so.
   */
  settle(fails: readonly boolean[] = [], older = true): void {
    for (let i = 0; i < 1000; i++) {
      this.do({ kind: "deliver", n: this.cache.queue.length });
      if (older) {
        this.t.older();
      }
      if (!this.waiting()) {
        this.do({ kind: "again" });
        this.do({ kind: "ask" });
        if (!this.waiting()) {
          return;
        }
      }
      if (fails[i] === true) {
        this.do({ kind: "fail" });
        continue;
      }
      this.do({ kind: "read" });
      this.do({ kind: "land" });
    }
    throw new Error("the thread kept asking");
  }
}

describe("the thread pane over any interleaving of reads and frames", () => {
  it("shows the thread as the cache holds it: the root, edited or deleted, and every reply", () => {
    fc.assert(
      fc.property(fc.nat(8), fc.nat(6), fc.array(step, { maxLength: 80 }), (replies, shown, steps) => {
        const run = new Run(replies, shown);
        for (const s of steps) {
          run.do(s);
        }
        run.settle();
        const view = run.t.view(run.rows());
        expect(view.rows.map(row)).toEqual(threadMessages(run.cache.ordered(), root).map(row));
        expect(view.start).toBe(true);
      }),
      {
        numRuns: 1000,
        examples: [
          // A reply's edit taken back while a page holding the edit was read.
          [
            1,
            0,
            [
              { kind: "ask" },
              { kind: "read" },
              { kind: "land" },
              { kind: "older" },
              { kind: "ask" },
              { kind: "write", what: 2, i: 1 },
              { kind: "deliver", n: 1 },
              { kind: "read" },
              { kind: "write", what: 3, i: 1 },
              { kind: "deliver", n: 1 },
              { kind: "land" },
            ],
          ],
        ],
      },
    );
  });

  it("reads again what it showed when told, keeping it shown meanwhile, whichever of its reads fail", () => {
    const lost = fc.record({ kind: fc.constant("write"), what: fc.nat(4), i: fc.nat(1000) });
    fc.assert(
      fc.property(
        fc.nat(8),
        fc.nat(6),
        fc.array(step, { maxLength: 40 }),
        fc.array(lost, { minLength: 1, maxLength: 4 }),
        fc.array(step, { maxLength: 20 }),
        fc.array(fc.boolean(), { maxLength: 6 }),
        (replies, shown, before, writes, after, fails) => {
          const run = new Run(replies, shown, true);
          run.settle(); // the whole thread read, as the window asked
          // Changes the stream loses, so what was read here is stale until read again;
          // and anything after, more reloads included. The window asks for nothing
          // older: what it showed is read again unasked, and stays shown meanwhile.
          const own = () => run.t.view([]).rows.map((m) => m.id); // what it read itself
          for (const s of [...before, ...writes, { kind: "readAgain" } as const, ...after]) {
            const kept = s.kind === "readAgain" ? own() : [];
            run.do(s);
            expect(own()).toEqual(expect.arrayContaining(kept));
          }
          run.settle(fails, false);
          const view = run.t.view(run.rows());
          expect(view.rows.map(row)).toEqual(threadMessages(run.cache.ordered(), root).map(row));
        },
      ),
      {
        numRuns: 1000,
        examples: [
          // A reply's edit taken back while the reload's page holding the edit was read.
          [
            1,
            0,
            [],
            [{ kind: "write", what: 2, i: 1 }],
            [
              { kind: "ask" },
              { kind: "read" },
              { kind: "land" },
              { kind: "ask" },
              { kind: "read" },
              { kind: "write", what: 3, i: 1 },
              { kind: "deliver", n: 1 },
              { kind: "land" },
            ],
            [],
          ],
        ],
      },
    );
  });
});
