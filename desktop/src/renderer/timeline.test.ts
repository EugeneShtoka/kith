import { clone, create } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";
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

/** A write to the cache. at picks a message by its place in the cache's order: 0 the oldest, -1 the newest. */
type Write =
  /** A new message, now. */
  | { what: "new" }
  /** History backfilled: a message from long ago. */
  | { what: "backfill"; ts: number }
  /** A message in another room. */
  | { what: "elsewhere" }
  /** Edited; its edit deleted, back to the first words; deleted. */
  | { what: "edit" | "unedit" | "delete"; at: number }
  /** Two votes, one after the other, each changing its poll's counts and this person's answers (a poll, if it was none). */
  | { what: "vote"; at: number }
  /** Its poll ended, or its question edited, its votes kept. */
  | { what: "end" | "retitle"; at: number }
  /** Moved in time (a network's order corrected). */
  | { what: "move"; at: number; ts: number }
  /** The newest n moved before every message. */
  | { what: "far"; n: number };

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
  /** Each network token handed out, by the message its page began at as it was then. */
  private readonly tokens = new Map<string, Message>();
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

  /** write is w written: whether it changed anything. */
  write(w: Write): boolean {
    this.clock++;
    switch (w.what) {
      case "new":
        this.put(this.fresh(1000 + this.clock * 10));
        return true;
      case "backfill":
        this.put(this.fresh(w.ts));
        return true;
      case "elsewhere":
        this.put(this.fresh(1000 + this.clock, "!other"));
        return true;
      case "far": {
        const newest = this.ordered().slice(-w.n);
        for (const m of newest) {
          const moved = clone(MessageSchema, m);
          moved.timestamp = timestampFromMs(0);
          this.put(moved);
        }
        return newest.length > 0;
      }
    }
    const m = this.ordered().at(w.at);
    if (m === undefined) {
      return false;
    }
    const next = clone(MessageSchema, m);
    switch (w.what) {
      case "edit":
        if (next.redacted) return false;
        next.edited = true;
        next.body = `${m.body}+`;
        next.editedAtUnixMs = BigInt(10_000 + this.clock);
        next.revisionId = `$e${String(this.clock)}`;
        break;
      case "unedit":
        if (next.redacted || !next.edited) return false;
        next.edited = false;
        next.body = m.body.replace(/\++$/, "");
        next.editedAtUnixMs = 0n;
        next.revisionId = "";
        break;
      case "delete":
        if (next.redacted) return false;
        next.redacted = true;
        next.body = "";
        next.editedAtUnixMs = 0n;
        next.revisionId = "";
        break;
      case "vote":
        if (next.redacted) return false;
        for (const k of [this.clock, this.clock + 1]) {
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
        return true;
      case "end":
        if (next.poll === undefined || next.poll.closed || next.redacted) return false;
        next.poll.closed = true;
        break;
      case "retitle":
        if (next.poll === undefined || next.redacted) return false;
        next.poll.question = `${next.poll.question}+`;
        next.edited = true;
        next.editedAtUnixMs = BigInt(10_000 + this.clock);
        next.revisionId = `$e${String(this.clock)}`;
        break;
      case "move":
        next.timestamp = timestampFromMs(w.ts);
        break;
    }
    this.put(next);
    return true;
  }

  /** page is what kithd answers ask with, read now. */
  page(ask: Ask): { rows: Message[]; next: string } {
    const all = this.ordered();
    const size = 4;
    switch (ask.kind) {
      case "latest":
        return { rows: all.slice(-size), next: "" };
      case "network": {
        // A network's token is a place in its history: a message moved since is not where it ends.
        const end = this.tokens.get(ask.from);
        const at = end === undefined ? all.length : all.filter((m) => compare(m, end) < 0).length;
        const from = Math.max(0, at - size);
        const first = all[from];
        if (from === 0 || first === undefined) {
          return { rows: all.slice(from, at), next: "" };
        }
        const next = `t${String(this.tokens.size)}`;
        this.tokens.set(next, clone(MessageSchema, first));
        return { rows: all.slice(from, at), next };
      }
      case "cached": {
        const at = ask.before === "" ? all.length : all.findIndex((m) => m.id === ask.before);
        // The cached page holds its anchor too, as MessagesAround does.
        return { rows: all.slice(Math.max(0, at - size), at + 1), next: "" };
      }
    }
  }
}

/** The steps a run interleaves. */
type Step =
  | { kind: "write"; write: Write }
  | { kind: "deliver"; n: number }
  | { kind: "ask" }
  | { kind: "read" }
  | { kind: "land" }
  | { kind: "fail" }
  | { kind: "again" }
  | { kind: "older" }
  | { kind: "readAgain" }
  /** A jump to a message the cache holds (at, as a write's), or with none to one it does not. */
  | { kind: "seek"; at?: number };

/** Run is one window over one cache: the page in flight, asked and maybe read. */
class Run {
  readonly cache: Cache;
  readonly t = new Timeline(room, "");
  private inFlight: { ticket: Ticket; read?: { rows: Message[]; next: string } } | undefined;
  /** Every ticket's answer, kept to land again late: one asked before a reload. */
  private stale: { ticket: Ticket; read: { rows: Message[]; next: string } }[] = [];
  /** The message the last jump was to, and whether a cached page failed since (which ends a jump, said as the failure). */
  sought: string | undefined;
  jumpFailed = false;
  /** A failed page is to be asked again after a wait, not over yet (the window's timer). */
  private later = false;
  /** That page waits: nothing asks for it before the wait is over but the window or kithd (a scroll, a jump, read_again). */
  private waits = false;

  constructor(initial: number) {
    this.cache = new Cache(initial);
  }

  /** idle: s would do nothing now, with no frame to deliver, no page to ask, read or answer, no wait to end. */
  idle(s: Step): boolean {
    switch (s.kind) {
      case "deliver":
        return this.cache.queue.length === 0;
      case "ask":
        return this.inFlight !== undefined;
      case "read":
        return this.inFlight === undefined || this.inFlight.read !== undefined;
      case "land":
        return this.inFlight?.read === undefined && this.stale.length === 0;
      case "fail":
        return this.inFlight === undefined && this.stale.length === 0;
      case "again":
        return !this.later;
      default:
        return false;
    }
  }

  /** do takes s: whether it did anything (a page to read, a frame to deliver). */
  do(s: Step): boolean {
    switch (s.kind) {
      case "write":
        return this.cache.write(s.write);
      case "deliver": {
        const frames = this.cache.queue.splice(0, s.n);
        for (const m of frames) {
          this.t.frame(m);
        }
        return frames.length > 0;
      }
      case "ask": {
        if (this.inFlight !== undefined) {
          return false;
        }
        const ticket = this.t.ask();
        if (ticket !== undefined && this.waits) {
          throw new Error("a refused page was asked again before its wait was over");
        }
        this.inFlight = ticket && { ticket };
        return ticket !== undefined;
      }
      case "read":
        if (this.inFlight === undefined || this.inFlight.read !== undefined) {
          return false;
        }
        this.inFlight.read = this.cache.page(this.inFlight.ticket.ask);
        return true;
      case "land": {
        const f = this.inFlight;
        if (f?.read !== undefined) {
          this.inFlight = undefined;
          this.t.land(f.ticket, f.read.rows, f.read.next);
          return true;
        }
        // An answer from before a reload, arriving now.
        const old = this.stale.shift();
        if (old === undefined) {
          return false;
        }
        this.t.land(old.ticket, old.read.rows, old.read.next);
        return true;
      }
      case "fail": {
        // The page in flight refused, or else an answer from before a reload, refused now.
        const ticket = this.inFlight?.ticket ?? this.stale.shift()?.ticket;
        if (ticket === undefined) {
          return false;
        }
        this.inFlight = undefined;
        this.jumpFailed ||= this.sought !== undefined && ticket.ask.kind === "cached";
        if (this.t.failed(ticket)) {
          this.later = true;
          this.waits = true;
        }
        return true;
      }
      case "again": // the wait after a failed page is over
        if (!this.later) {
          return false;
        }
        this.later = false;
        this.waits = false;
        this.t.again();
        return true;
      case "older":
        this.waits = false;
        this.t.older();
        return true;
      case "seek": {
        const to = s.at === undefined ? undefined : this.cache.ordered().at(s.at);
        if (s.at !== undefined && to === undefined) {
          return false;
        }
        this.sought = to?.id ?? "$gone";
        this.jumpFailed = false;
        this.waits = false;
        this.t.seek(this.sought);
        return true;
      }
      case "readAgain":
        // The stream could not resume: every frame not delivered is lost, and kithd
        // says to read again. An answer in flight still arrives, later.
        this.cache.queue = [];
        this.waits = false;
        if (this.inFlight !== undefined) {
          const f = this.inFlight;
          this.stale.push({ ticket: f.ticket, read: f.read ?? this.cache.page(f.ticket.ask) });
          this.inFlight = undefined;
        }
        this.t.reload();
        return true;
    }
  }

  /** waiting: a page is in flight. */
  private waiting(): boolean {
    return this.inFlight !== undefined;
  }

  /**
   * settle delivers every frame and answers every page due, asking for no older one, and
   * waits out every wait, asking before each is over too (nothing may be asked then).
   */
  settle(): void {
    for (let i = 0; i < 1000; i++) {
      this.do({ kind: "deliver", n: this.cache.queue.length });
      if (!this.waiting()) {
        this.do({ kind: "ask" });
      }
      if (!this.waiting()) {
        this.do({ kind: "again" });
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

/** Where a search starts: a room as it opens, no page read yet. */
const opening = (initial: number) => new Run(initial);

/** shown: the room's newest page shown. */
const shown = (initial: number) => {
  const run = new Run(initial);
  run.settle();
  return run;
};

/** scrolled: the room scrolled back a page. */
const scrolled = (initial: number) => {
  const run = shown(initial);
  run.do({ kind: "older" });
  run.settle();
  return run;
};

/** asking: the room shown, and the page before it asked for, not read yet. */
const asking = (initial: number) => {
  const run = shown(initial);
  run.do({ kind: "older" });
  run.do({ kind: "ask" });
  return run;
};

/** offline: the room shown, its network's page refused, so older pages are the cache's. */
const offline = (initial: number) => {
  const run = shown(initial);
  run.do({ kind: "older" });
  run.do({ kind: "ask" });
  run.do({ kind: "fail" });
  return run;
};

/** reloading: the room shown, a new message whose frame is lost, and read_again. */
const reloading = (initial: number) => {
  const run = shown(initial);
  run.do({ kind: "write", write: { what: "new" } });
  run.do({ kind: "readAgain" });
  return run;
};

/** frozen: v is a message, which neither the cache nor the timeline changes once made (each writes a new one). */
const frozen = (v: object): v is Message => "$typeName" in v;

/**
 * copied is a deep copy of v, each object copied once so what was shared stays shared
 * (the ticket the run holds and the timeline's own), class instances keeping their
 * class; messages are shared, never changed.
 */
function copied<T>(v: T, seen = new Map<object, unknown>()): T {
  if (typeof v !== "object" || v === null || frozen(v)) {
    return v;
  }
  const done = seen.get(v);
  if (done !== undefined) {
    return done as T;
  }
  if (v instanceof Map) {
    const m = new Map();
    seen.set(v, m);
    for (const [k, x] of v) {
      m.set(copied(k, seen), copied(x, seen));
    }
    return m as T;
  }
  if (Array.isArray(v)) {
    const a: unknown[] = [];
    seen.set(v, a);
    for (const x of v) {
      a.push(copied(x, seen));
    }
    return a as T;
  }
  const o = Object.create(Object.getPrototypeOf(v) as object) as Record<string, unknown>;
  seen.set(v, o);
  for (const [k, x] of Object.entries(v)) {
    o[k] = copied(x, seen);
  }
  return o as T;
}

/** written holds each message as a short name for its content, made once: messages do not change. */
const written = new WeakMap<Message, string>();
const contents = new Map<string, string>();

/**
 * state is v written out whole: equal for two runs that will do the same from here on.
 * A message is its content, an object met again is the same one, and the timeline's
 * view, made from the rest, is left out.
 */
function state(v: unknown, seen = new Map<object, number>()): string {
  if (typeof v === "bigint") {
    return `${String(v)}n`;
  }
  if (v === undefined) {
    return "u";
  }
  if (typeof v !== "object" || v === null) {
    return JSON.stringify(v);
  }
  if (frozen(v)) {
    let w = written.get(v);
    if (w === undefined) {
      const content = `{${Object.entries(v)
        .map(([k, x]) => `${k}:${state(x, new Map())}`)
        .join(",")}}`;
      w = contents.get(content) ?? `#${String(contents.size)}`;
      contents.set(content, w);
      written.set(v, w);
    }
    return w;
  }
  const at = seen.get(v);
  if (at !== undefined) {
    return `@${String(at)}`; // the same object again, not one equal to it
  }
  seen.set(v, seen.size);
  if (v instanceof Map) {
    return `M[${[...v].map(([k, x]) => `${state(k, seen)}:${state(x, seen)}`).join(",")}]`;
  }
  if (Array.isArray(v)) {
    return `[${v.map((x) => state(x, seen)).join(",")}]`;
  }
  return `{${Object.entries(v)
    .filter(([k]) => k !== "snapshot")
    .map(([k, x]) => `${k}:${state(x, seen)}`)
    .join(",")}}`;
}

/**
 * explore takes every sequence of up to depth steps from start, settles each and checks
 * it: every interleaving within the bound, the same ones on every run. A step that does
 * nothing where it falls (no frame to deliver, no page to read) is not taken, and a
 * state reached before, by as few steps, is not explored again.
 */
function explore(start: Run, steps: readonly Step[], depth: number, check: (run: Run) => void): void {
  const reached = new Map<string, number>();
  const walk = (run: Run, path: readonly Step[]): void => {
    const key = state(run);
    const before = reached.get(key);
    if (before !== undefined && before <= path.length) {
      return;
    }
    reached.set(key, path.length);
    const settled = copied(run);
    settled.settle();
    try {
      check(settled);
    } catch (err) {
      if (err instanceof Error) {
        err.message = `after ${JSON.stringify(path)}: ${err.message}`;
      }
      throw err;
    }
    if (path.length < depth) {
      for (const s of steps) {
        if (run.idle(s)) {
          continue;
        }
        const next = copied(run);
        if (next.do(s)) {
          walk(next, [...path, s]);
        }
      }
    }
  };
  walk(start, []);
}

const write = (w: Write): Step => ({ kind: "write", write: w });

/** pages are the window's and kithd's side: pages asked, read, landed or refused, frames delivered, a scroll. */
const pages: readonly Step[] = [
  { kind: "ask" },
  { kind: "read" },
  { kind: "land" },
  { kind: "fail" },
  { kind: "again" },
  { kind: "older" },
  { kind: "deliver", n: 1 },
];

/** moves are writes that change the cache's order: a new message, one moved back or forward, the newest moved before all. */
const moves: readonly Step[] = [
  write({ what: "new" }),
  write({ what: "move", at: -1, ts: 15 }),
  write({ what: "move", at: 0, ts: 5000 }),
  write({ what: "far", n: 1 }),
  write({ what: "far", n: 4 }),
  write({ what: "far", n: 9 }),
];

/** edits change a message in place, or write one outside what is shown. */
const edits: readonly Step[] = [
  write({ what: "edit", at: -1 }),
  write({ what: "edit", at: 0 }),
  write({ what: "unedit", at: -1 }),
  write({ what: "delete", at: -1 }),
  write({ what: "backfill", ts: 15 }),
  write({ what: "elsewhere" }),
];

/** polls are votes, a poll ended and its question edited. */
const polls: readonly Step[] = [
  write({ what: "vote", at: -1 }),
  write({ what: "vote", at: 0 }),
  write({ what: "end", at: -1 }),
  write({ what: "retitle", at: -1 }),
];

/** drawn is a row as the table writes it, and its poll whole: every answer's count and mark, and whether it ended. */
const drawn = (m: Message) => ({ ...row(m), poll: m.poll });

/** shownMatchesCache: what is shown is the cache's newest messages, in its order, as it holds them. */
function shownMatchesCache(run: Run): void {
  const shown = run.t.view().rows.map(drawn);
  const cache = run.cache.ordered().map(drawn);
  const newest = cache.slice(cache.length - shown.length);
  if (state(shown) !== state(newest)) {
    expect(shown).toEqual(newest); // says how they differ
  }
  if (cache.length > 0 && shown.length === 0) {
    expect(shown.length).toBeGreaterThan(0);
  }
}

/** jumpFound: the last jump's message is the target and a row where the cache holds it, and said missing where not. */
function jumpFound(run: Run): void {
  if (run.sought === undefined) {
    return;
  }
  const held = run.cache.ordered().some((m) => m.id === run.sought);
  const v = run.t.view();
  if (run.jumpFailed && v.target === "") {
    expect(run.t.jumpMissed()).toBe(false); // the failed page is what is said
    return;
  }
  expect(v.target).toBe(held ? run.sought : "");
  expect(v.rows.some((m) => m.id === v.target)).toBe(held);
  // Held, it may still have been said missing: written after the last page was read, it
  // came as a frame once the pages ran out.
  if (!held) {
    expect(run.t.jumpMissed()).toBe(true);
  }
}

describe("the timeline over every interleaving of pages and frames, within a bound", () => {
  // Each search is exhaustive within its bound and the same on every run; its steps are
  // the ones its concern turns on, so the bound reaches the depth that concern needs.
  const searched = (
    name: string,
    starts: readonly Run[],
    steps: readonly Step[],
    depth: number,
    check: (run: Run) => void,
  ) => {
    it(name, { timeout: 60_000 }, () => {
      for (const start of starts) {
        explore(start, steps, depth, check);
      }
    });
  };

  searched(
    "shows the cache's newest messages in its order, as pages land, fail and frames move rows",
    [opening(0), opening(1), opening(5), shown(1), shown(5), scrolled(9), asking(5), offline(5)],
    [...pages, ...moves],
    4,
    shownMatchesCache,
  );

  searched(
    "shows each message as the cache holds it, edited, deleted or written outside the rows",
    [opening(3), shown(5), scrolled(9)],
    [...pages, ...edits],
    3,
    shownMatchesCache,
  );

  searched(
    "keeps each poll shown as the cache holds it, its votes, end and edits coming before and after pages",
    [shown(1), shown(5)],
    [...pages, ...polls, write({ what: "new" }), write({ what: "delete", at: -1 })],
    4,
    shownMatchesCache,
  );

  searched(
    "reads again to the cache when told, the changes lost, rows moved meanwhile and whichever of its pages fail",
    [shown(5), offline(5), reloading(5)],
    [...pages, { kind: "readAgain" }, write({ what: "edit", at: -1 }), ...moves],
    4,
    shownMatchesCache,
  );

  searched(
    "finds a jump's message wherever the cache holds it, reading back to it, and says when it holds none",
    [opening(5), shown(5), scrolled(9)],
    [
      ...pages,
      { kind: "seek", at: -1 },
      { kind: "seek", at: 0 },
      { kind: "seek" },
      write({ what: "new" }),
      write({ what: "move", at: -1, ts: 15 }),
      write({ what: "far", n: 9 }),
    ],
    4,
    (run) => {
      shownMatchesCache(run);
      jumpFound(run);
    },
  );
});

describe("the timeline, case by case", () => {
  it("keeps a page read before a frame from taking the frame's change back", () => {
    const run = new Run(3);
    run.do({ kind: "ask" });
    run.do({ kind: "read" }); // the cache as it was
    run.do({ kind: "write", write: { what: "edit", at: 2 } }); // the newest edited
    run.do({ kind: "deliver", n: 1 });
    run.do({ kind: "land" });
    shownMatchesCache(run);
  });

  it("leaves out an edit of a message older than every row, and shows it once a page reaches it", () => {
    const run = new Run(12);
    run.settle();
    const before = run.t.view().rows.map((m) => m.id);
    run.do({ kind: "write", write: { what: "edit", at: 0 } }); // the oldest message edited
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

  it("reads the room again from its newest when a frame moves every row shown below them", () => {
    const run = new Run(1);
    run.settle(); // its one message shown, and the network has no older page
    run.do({ kind: "older" });
    run.do({ kind: "ask" });
    run.do({ kind: "fail" }); // the older page refused: none is asked until scrolled again
    run.do({ kind: "write", write: { what: "far", n: 1 } }); // the message moved before the stretch shown
    run.do({ kind: "deliver", n: 1 });
    expect(run.t.loaded).toBe(false); // nothing shown is the opening's state again
    run.do({ kind: "ask" });
    run.do({ kind: "fail" }); // and the page asked for refused: asked again after a wait
    run.settle();
    expect(run.t.view().rows).toHaveLength(1);
    shownMatchesCache(run);
  });

  it("reads back again to a jump's message found after a failed page, once it moves out of the rows", () => {
    const run = offline(5); // the network refused: older pages are the cache's
    run.do({ kind: "seek", at: 0 });
    run.do({ kind: "ask" });
    run.do({ kind: "fail" }); // the jump's page refused: it reads no further
    run.do({ kind: "older" });
    run.settle(); // scrolled back, and its message is a row after all
    expect(run.t.view().target).toBe(run.sought);
    run.do({ kind: "write", write: { what: "move", at: 0, ts: 0 } }); // moved below the rows
    run.settle();
    expect(run.t.view().target).toBe(run.sought);
    expect(run.t.view().rows.some((m) => m.id === run.sought)).toBe(true);
  });

  it("ignores a frame for a room not open", () => {
    const run = new Run(3);
    run.settle();
    run.do({ kind: "write", write: { what: "elsewhere" } });
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
      run.do({ kind: "write", write: { what: "edit", at: 30 - reached + i } });
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
    run.do({ kind: "write", write: { what: "edit", at: 3 } }); // one of those edited
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
    run.do({ kind: "write", write: { what: "edit", at: 2 } });
    run.do({ kind: "deliver", n: 1 });
    run.do({ kind: "write", write: { what: "edit", at: 2 } }); // edited again
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
