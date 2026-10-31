// One open room's messages as the window shows them: pages read from kithd and the
// Messages stream's frames, merged so what is shown is always a stretch of the cache,
// up to its newest message, each row as the cache holds it once the frames settle.
//
// Frames are the cache's changes in the order it wrote them (design-replay.md), every
// one after the stream's start: so a row shown is kept current by its frames, a page
// only adds rows and fills in. What a page cannot know is what changed while it was
// read: the frames that arrive meanwhile are put back over it when it lands.
import type { Message, Reaction, ReactionUpdate } from "../gen/backend/v1/types_pb";
import { compare, merge, place } from "./merge";

/** A page the timeline asks for: one at a time, each when the last has landed. */
export type Ask =
  /** CachedTimeline: the room's newest cached messages, what it opens with. */
  | { kind: "latest" }
  /** Timeline: the network's page from a token ("" its newest). */
  | { kind: "network"; from: string }
  /** MessagesAround: the cached page before a message shown. */
  | { kind: "cached"; before: string };

/** An ask in flight: an answer is used only while its ticket is the one in flight. */
export interface Ticket {
  ask: Ask;
}

/**
 * The lowest message shown: every cached message at or after it is shown, and none
 * before. "all" is every message there is; undefined, none yet (no page landed).
 */
type Floor = Message | "all" | undefined;

/** A stretch of the cache: its rows, its floor, and whether it reaches the room's start. */
interface Stretch {
  rows: readonly Message[];
  floor: Floor;
  start: boolean;
}

/** What the window draws of the open room. */
export interface TimelineView {
  room: string;
  rows: readonly Message[];
  /** The room's first message is shown: there is nothing older to ask for. */
  start: boolean;
  /** A page is on its way. */
  loading: boolean;
  /** Reactions by the message they annotate. */
  reactions: ReadonlyMap<string, readonly Reaction[]>;
  /** The message each reply answers, where it is not a row (fetched by ID). */
  quotes: ReadonlyMap<string, Message | undefined>;
  /** The last message read when the room was opened: the unread line follows it. */
  unreadFrom: string;
  /** The row a jump found, to show and mark ("" none), and how many jumps there were: each is shown once. */
  target: string;
  jumps: number;
}

/** inside: m belongs to the stretch, at or after its floor. */
function inside(s: Stretch, m: Message): boolean {
  return s.floor === "all" || s.start || (s.floor !== undefined && compare(m, s.floor) >= 0);
}

/** apply is s's rows with frame m as the cache now holds it: in place, or out when below the floor. */
function apply(s: Stretch, m: Message): readonly Message[] {
  const others = s.rows.some((r) => r.id === m.id) ? s.rows.filter((r) => r.id !== m.id) : s.rows;
  return inside(s, m) ? place(others, m) : others;
}

/** oldest is the first of page in timeline order. */
function oldest(page: readonly Message[]): Message | undefined {
  return page.reduce<Message | undefined>((o, m) => (o === undefined || compare(m, o) < 0 ? m : o), undefined);
}

/** lower is the floor reaching down to m too. */
function lower(floor: Floor, m: Message | undefined): Floor {
  if (m === undefined || floor === "all") {
    return floor;
  }
  return floor === undefined || compare(m, floor) < 0 ? m : floor;
}

/** short: the stretch's floor is above to, so it reaches less far back. */
function short(s: Stretch, to: Floor): boolean {
  if (s.start || s.floor === "all" || to === undefined) {
    return false;
  }
  return s.floor === undefined || to === "all" || compare(s.floor, to) > 0;
}

export class Timeline {
  private shown: Stretch = { rows: [], floor: undefined, start: false };
  /** The network's next page; undefined until its newest was asked for. */
  private token: string | undefined;
  /** The network has no older page: older pages are the cache's. */
  private cached = false;
  /** Scrolled to the top: one older page is wanted. */
  private wantOlder = false;
  /**
   * A page failed: none is asked for again until the window asks (older, reload), or, a
   * reload's, until the wait after it is over (again).
   */
  private stalled = false;
  private asked: Ticket | undefined;
  /** The frames since the page in flight was asked, or since a reload began. */
  private since: Message[] | undefined;
  /** A reload: the stretch read again, shown once it reaches as far back as to. */
  private reloading: { into: Stretch; to: Floor } | undefined;
  private reactions = new Map<string, readonly Reaction[]>();
  private quotes = new Map<string, Message | undefined>();
  /**
   * The message a jump is to, while older pages are read until it is a row; and whether
   * none is left to read, so only a frame can bring it (one written after the page).
   */
  private seeking: string | undefined;
  private readAll = false;
  private target = "";
  private jumps = 0;
  /** A jump that found nothing: the message is not in the cache. */
  private missed = false;
  /** The thread a jump's message is a reply in, its pane to open ("" none). */
  private replyIn = "";
  private snapshot: TimelineView | undefined;
  /** The newest message a receipt landed for, and the one in flight. */
  marked = "";
  sending = "";

  constructor(
    readonly room: string,
    private readonly unreadFrom: string,
  ) {}

  /** view is what the window draws, the same object until something changes. */
  view(): TimelineView {
    this.snapshot ??= {
      room: this.room,
      rows: this.shown.rows,
      start: this.shown.start,
      loading: this.asked !== undefined,
      reactions: this.reactions,
      quotes: this.quotes,
      unreadFrom: this.unreadFrom,
      target: this.target,
      jumps: this.jumps,
    };
    return this.snapshot;
  }

  /** loaded: the first page has landed. */
  get loaded(): boolean {
    return this.shown.floor !== undefined;
  }

  /** ask is the next page to read, now in flight, or undefined when none is due. */
  ask(): Ticket | undefined {
    const ask = this.next();
    if (ask === undefined) {
      return undefined;
    }
    this.asked = { ask };
    this.since ??= [];
    this.changed();
    return this.asked;
  }

  private next(): Ask | undefined {
    if (this.asked !== undefined || this.stalled) {
      return undefined;
    }
    const r = this.reloading;
    if (r !== undefined) {
      const first = r.into.rows[0];
      if (r.into.floor === undefined) {
        return { kind: "latest" };
      }
      return first === undefined ? undefined : { kind: "cached", before: first.id };
    }
    if (this.shown.floor === undefined) {
      return { kind: "latest" };
    }
    const first = this.shown.rows[0];
    if (this.seeking !== undefined && !this.readAll && first !== undefined) {
      // A jump reads the cache back to its message: it is a cached message (a search
      // hit), and the network's pages would reach it fifty at a time.
      return { kind: "cached", before: first.id };
    }
    if (this.token === undefined && !this.cached) {
      return { kind: "network", from: "" };
    }
    if (!this.wantOlder || this.shown.start) {
      return undefined;
    }
    if (!this.cached) {
      return { kind: "network", from: this.token ?? "" };
    }
    return first === undefined ? undefined : { kind: "cached", before: first.id };
  }

  /** older asks for the page before what is shown: the window was scrolled to its top. */
  older(): void {
    this.wantOlder = true;
    this.stalled = false;
  }

  /**
   * frame is the Messages stream's m, as the cache now holds it: it replaces the row with
   * its ID, joins the rows when at or after the floor, and leaves them when moved below.
   * A message older than every row shown stays out: the page that reaches it brings it,
   * and an old message shown alone at the top would hide all between.
   */
  frame(m: Message): void {
    if (m.roomId !== this.room) {
      return;
    }
    this.since?.push(m);
    if (this.quotes.has(m.id)) {
      this.quotes = new Map(this.quotes).set(m.id, m); // a quote changes as its message does
    }
    this.shown = { ...this.shown, rows: apply(this.shown, m) };
    if (m.id === this.seeking) {
      this.sought();
    }
    this.changed();
  }

  /**
   * land merges the page t asked for: rows it adds, the floor lowered to its oldest,
   * then the frames that arrived while it was read put back over it, since the page may
   * predate them. next is a network page's token.
   */
  land(t: Ticket, page: readonly Message[], next = "", reactions: readonly Reaction[] = []): void {
    if (t !== this.asked) {
      return; // asked before a reload: what it read may predate what the stream lost
    }
    this.asked = undefined;
    const r = this.reloading;
    const s: Stretch = { ...(r?.into ?? this.shown) };
    const had = new Set(s.rows.map((m) => m.id));
    s.rows = merge(s.rows, page);
    switch (t.ask.kind) {
      case "latest":
        s.floor = page.length > 0 ? oldest(page) : "all";
        break;
      case "network":
        s.floor = lower(s.floor, oldest(page));
        this.token = next;
        this.cached = next === "";
        this.wantOlder = false;
        break;
      case "cached":
        if (page.every((m) => had.has(m.id))) {
          s.start = true; // a cached page that adds nothing: the room's start
        } else {
          s.floor = lower(s.floor, oldest(page));
        }
        this.wantOlder = false;
        break;
    }
    this.react(...reactions);
    if (r !== undefined) {
      r.into = s;
      if (short(s, r.to)) {
        this.changed();
        return; // the reload reads on until it reaches as far back as was shown
      }
      this.reloading = undefined;
    }
    this.shown = this.replayed(s);
    this.sought();
    this.changed();
  }

  /** replayed is s with the frames since the ask put back over it, in order. */
  private replayed(s: Stretch): Stretch {
    for (const m of this.since ?? []) {
      s = { ...s, rows: apply(s, m) };
    }
    this.since = undefined;
    return s;
  }

  /**
   * failed is t's page refused. The network's hands over to the cache's (a network off
   * or unreachable has no page to give); any other stops asking until the window does.
   * A reload's is to be asked again after a wait (again), said by the answer: nothing
   * else asks for it, and until it is read the rows shown may be stale.
   */
  failed(t: Ticket): boolean {
    if (t !== this.asked) {
      return false;
    }
    this.asked = undefined;
    if (t.ask.kind === "network") {
      this.cached = true;
    } else {
      this.stalled = true;
    }
    if (this.reloading === undefined) {
      this.since = undefined; // every frame meanwhile is already shown
    }
    this.sought();
    this.changed();
    return this.stalled && this.reloading !== undefined;
  }

  /** again asks for a reload's failed page again: the wait after it is over. */
  again(): void {
    if (this.reloading !== undefined) {
      this.stalled = false;
    }
  }

  /**
   * seek is a jump to the message id: once it is a row it is the target, shown and
   * marked; until then older cached pages are read, back to the room's start.
   */
  seek(id: string): void {
    this.seeking = id;
    this.readAll = false;
    this.jumps++;
    this.target = "";
    this.missed = false;
    this.replyIn = "";
    this.stalled = false;
    this.sought();
    this.changed();
  }

  /** jumpThread is the thread the last jump's message is a reply in, its pane to open: said once. */
  jumpThread(): string {
    const root = this.replyIn;
    this.replyIn = "";
    return root;
  }

  /** jumpMissed: the last jump found nothing, said once. */
  jumpMissed(): boolean {
    const missed = this.missed;
    this.missed = false;
    return missed;
  }

  /**
   * sought ends the jump once its message is a row (a reply in a thread shows its root,
   * and its thread is to open),
   * or once nothing older is left to read for it.
   */
  private sought(): void {
    const id = this.seeking;
    if (id === undefined || this.shown.floor === undefined || this.reloading !== undefined) {
      return;
    }
    const found = this.shown.rows.find((m) => m.id === id);
    if (found !== undefined && found.threadRoot !== "" && found.threadRoot !== id) {
      this.replyIn = found.threadRoot;
      this.seeking = found.threadRoot;
      this.readAll = false;
      this.sought();
      return;
    }
    if (found !== undefined) {
      this.seeking = undefined;
      this.target = id;
    } else if (!this.readAll && (this.shown.start || this.shown.floor === "all" || this.stalled)) {
      // Said once; a frame may still bring it, written after the page that ran out.
      this.readAll = true;
      this.missed = !this.stalled; // a page that failed says so itself
    }
  }

  /**
   * reload is the stream's read_again: frames were lost, so every row shown may be stale.
   * The stretch shown is read again as far back as it reached, and replaces it once
   * read; it stays on screen meanwhile, frames still applied. Pages asked before are
   * not used, nor frames from before, and reply targets are fetched again.
   */
  reload(): void {
    this.asked = undefined; // its answer is not used
    this.stalled = false;
    this.since = [];
    this.quotes = new Map();
    if (this.shown.floor !== undefined) {
      const to = this.reloading?.to ?? (this.shown.start ? "all" : this.shown.floor);
      this.reloading = { into: { rows: [], floor: undefined, start: false }, to };
    }
    this.changed();
  }

  /** reactionsRead replaces the room's reactions with what CachedReactions read. */
  reactionsRead(all: readonly Reaction[]): void {
    this.reactions = new Map();
    this.react(...all);
    this.changed();
  }

  /** reacted applies one reaction arriving or going (the Reactions stream). */
  reacted(u: ReactionUpdate): void {
    const r = u.reaction;
    if (r?.roomId !== this.room) {
      return;
    }
    if (u.removed) {
      const list = this.reactions.get(r.target) ?? [];
      if (list.some((x) => x.id === r.id)) {
        this.reactions = new Map(this.reactions).set(
          r.target,
          list.filter((x) => x.id !== r.id),
        );
      }
    } else {
      this.react(r);
    }
    this.changed();
  }

  /** react adds reactions, each once by its ID (tui withReactions). */
  private react(...rs: Reaction[]): void {
    let next: Map<string, readonly Reaction[]> | undefined;
    for (const r of rs) {
      const list = (next ?? this.reactions).get(r.target) ?? [];
      if (list.some((x) => x.id === r.id)) {
        continue;
      }
      next ??= new Map(this.reactions);
      next.set(r.target, [...list, r]);
    }
    if (next !== undefined) {
      this.reactions = next;
    }
  }

  /** quoted is the message a reply answers, if known: a row, or one fetched. */
  quoted(id: string): Message | undefined {
    return this.shown.rows.find((m) => m.id === id) ?? this.quotes.get(id);
  }

  /** unquoted are the reply targets neither shown nor asked for, now asked for. */
  unquoted(most: number): string[] {
    const shown = new Set(this.shown.rows.map((m) => m.id));
    const wanted: string[] = [];
    for (const m of this.shown.rows) {
      const target = m.replyTo;
      if (wanted.length >= most) {
        break;
      }
      if (target !== "" && !shown.has(target) && !this.quotes.has(target) && !wanted.includes(target)) {
        wanted.push(target);
      }
    }
    if (wanted.length > 0) {
      this.quotes = new Map(this.quotes);
      for (const id of wanted) {
        this.quotes.set(id, undefined);
      }
      this.changed();
    }
    return wanted;
  }

  /**
   * fetchedQuote files a reply target FetchEvent read, unless a frame for it came while
   * it was read: the frame is the newer.
   */
  fetchedQuote(m: Message): void {
    if (this.quotes.has(m.id) && this.quotes.get(m.id) === undefined) {
      this.quotes = new Map(this.quotes).set(m.id, m);
      this.changed();
    }
  }

  private changed(): void {
    this.snapshot = undefined;
  }
}

/** newest is the newest message of the main timeline (not in a thread): what a receipt is for. */
export function newest(rows: readonly Message[]): Message | undefined {
  return rows.findLast((m) => m.threadRoot === "" && m.id !== "");
}

/**
 * tally counts a message's reactions by key, in the order each key first appears, each
 * once by its ID (domain.AggregateReactions; conformance table
 * internal/domain/testdata/reactions.json).
 */
export function tally(rs: readonly Reaction[]): { key: string; count: number }[] {
  const seen = new Set<string>();
  const counts = new Map<string, number>();
  for (const r of rs) {
    if (r.key === "" || (r.id !== "" && seen.has(r.id))) {
      continue;
    }
    if (r.id !== "") {
      seen.add(r.id);
    }
    counts.set(r.key, (counts.get(r.key) ?? 0) + 1);
  }
  return [...counts].map(([key, count]) => ({ key, count }));
}
