// Threads as the TUI groups them (domain.Conversations, CollapseThreads and
// ThreadMessages), held to Go by the conformance table internal/domain/testdata/threads.json:
// a thread's replies leave the main timeline for a summary under the message it hangs
// from, and its pane shows the root and the replies.
import type { Message } from "../gen/backend/v1/types_pb";
import { compare, compareBytes, merge, nanos, place } from "./merge";

/** A thread's summary, enough to draw its row in the main timeline. */
export interface ThreadSummary {
  root: string;
  /** Replies, the root not counted, and the newest of them. */
  count: number;
  latest: string;
  latestSender: string;
  latestSenderName: string;
  /** The main-timeline message the row hangs under ("" above the first); the root when loaded. */
  anchor: string;
  rootLoaded: boolean;
}

/**
 * conversations maps each loaded message in a thread to its root: a root to itself, and
 * a plain reply to a threaded message joins that thread.
 */
export function conversations(msgs: readonly Message[]): Map<string, string> {
  const roots = new Set(msgs.filter((m) => m.threadRoot !== "").map((m) => m.threadRoot));
  const into = new Map<string, string>();
  if (roots.size === 0) {
    return into;
  }
  for (const m of msgs) {
    if (m.threadRoot !== "") {
      into.set(m.id, m.threadRoot);
    } else if (roots.has(m.id)) {
      into.set(m.id, m.id);
    } else if (m.replyTo !== "") {
      const root = into.get(m.replyTo);
      if (root !== undefined) {
        into.set(m.id, root);
      }
    }
  }
  return into;
}

/** collapse splits a room's messages, in timeline order, into the main timeline and its threads. */
export function collapse(msgs: readonly Message[]): { main: Message[]; threads: ThreadSummary[] } {
  const into = conversations(msgs);
  const main: Message[] = [];
  const byRoot = new Map<string, ThreadSummary>();
  /** When each thread's newest reply came. */
  const at = new Map<string, bigint>();
  for (const m of msgs) {
    const root = into.get(m.id);
    if (root === undefined || root === m.id) {
      main.push(m);
      continue;
    }
    let t = byRoot.get(root);
    if (t === undefined) {
      t = { root, count: 0, latest: "", latestSender: "", latestSenderName: "", anchor: "", rootLoaded: false };
      byRoot.set(root, t);
    }
    t.count++;
    t.latest = m.id;
    at.set(root, nanos(m));
    t.latestSender = m.sender;
    t.latestSenderName = m.senderName;
    t.anchor = main.at(-1)?.id ?? "";
  }
  const loaded = new Set(main.map((m) => m.id));
  const threads = [...byRoot.values()].map((t) =>
    loaded.has(t.root) ? { ...t, rootLoaded: true, anchor: t.root } : t,
  );
  const when = (t: ThreadSummary) => at.get(t.root) ?? 0n;
  threads.sort((a, b) => (when(a) === when(b) ? compareBytes(a.root, b.root) : when(a) < when(b) ? -1 : 1));
  return { main, threads };
}

/** threadMessages is a thread's root, when loaded, and its replies, in timeline order. */
export function threadMessages(msgs: readonly Message[], root: string): Message[] {
  if (root === "") {
    return [];
  }
  const into = conversations(msgs);
  const first = msgs.find((m) => m.id === root);
  return [...(first === undefined ? [] : [first]), ...msgs.filter((m) => m.id !== root && into.get(m.id) === root)];
}
/** A page of a thread's replies the pane asks for, or its root: one of each at a time. */
export type ThreadAsk = { kind: "page"; from: string } | { kind: "root" };

/** An ask in flight: its answer is used only while it is the one in flight. */
export interface ThreadTicket {
  ask: ThreadAsk;
}

/** What the window draws of the open thread. */
export interface ThreadView {
  room: string;
  root: string;
  /** The root, when known, and the replies, in timeline order, each as the cache holds it. */
  rows: readonly Message[];
  /** Every reply is read: no older page is left. */
  start: boolean;
  loading: boolean;
}

/** What the pane read itself: the thread's messages beyond the room's rows, and how far. */
interface Read {
  own: readonly Message[];
  /** The next page's token: undefined before the newest was asked, "" once none is left. */
  token: string | undefined;
  /** The oldest message a page brought. */
  oldest: Message | undefined;
  rootAsked: boolean;
}

const nothingRead: Read = { own: [], token: undefined, oldest: undefined, rootAsked: false };

/**
 * OpenThread is the thread the pane shows: what the room's timeline holds of it, and
 * what it read itself (its root, ThreadPage's older replies), kept by the Messages
 * stream's frames as the timeline is. A page read before a frame is put back under it
 * when it lands, so a reply or the root edited or deleted meanwhile stays so.
 */
export class OpenThread {
  private read: Read = nothingRead;
  /**
   * A reload: what was read here, read again into a side copy, shown once it reaches as
   * far back as the pages had (to: the oldest they brought, "all" every reply) and holds
   * the root again if it was read.
   */
  private reloading: { into: Read; to: Message | "all" | undefined; root: boolean } | undefined;
  private wantOlder = false;
  /**
   * A read failed: none is asked again until the window asks (older), or, a reload's,
   * until the wait after it is over (again).
   */
  private stalled = false;
  private asked: ThreadTicket | undefined;
  /** The frames since the ask in flight was made. */
  private since: Message[] | undefined;
  /** The newest reply a receipt landed for, and the one in flight. */
  marked = "";
  sending = "";

  constructor(
    readonly room: string,
    readonly root: string,
  ) {}

  /** view is the thread drawn over the room's rows. */
  view(rows: readonly Message[]): ThreadView {
    const ids = new Set(rows.map((m) => m.id));
    const all = merge(
      rows,
      this.read.own.filter((m) => !ids.has(m.id)),
    );
    return {
      room: this.room,
      root: this.root,
      rows: threadMessages(all, this.root),
      start: this.read.token === "",
      loading: this.asked !== undefined,
    };
  }

  /**
   * ask is the next read, now in flight: a reload's, else the root when neither shown
   * nor read, then pages.
   */
  ask(rows: readonly Message[]): ThreadTicket | undefined {
    if (this.asked !== undefined || this.stalled) {
      return undefined;
    }
    const read = this.read;
    let ask: ThreadAsk | undefined;
    if (this.reloading !== undefined) {
      ask = due(this.reloading); // only the reload's reads: it is shown once they are read
    } else if (!read.rootAsked && !rows.some((m) => m.id === this.root) && !read.own.some((m) => m.id === this.root)) {
      ask = { kind: "root" };
    } else if (this.wantOlder && read.token !== "") {
      // Pages only when asked: a thread's replies are the network's to give (Matrix's
      // relations), and what the room holds of them is shown already.
      ask = { kind: "page", from: read.token ?? "" };
    }
    if (ask === undefined) {
      return undefined;
    }
    this.asked = { ask };
    this.since = [];
    return this.asked;
  }

  /** older asks for the page of replies before those shown. */
  older(): void {
    this.wantOlder = true;
    this.stalled = false;
  }

  /** frame is the Messages stream's m: it replaces its row here, or joins as the root or a reply. */
  frame(m: Message): void {
    if (m.roomId !== this.room) {
      return;
    }
    this.since?.push(m);
    this.read = { ...this.read, own: put(this.read.own, m, this.root) };
    const r = this.reloading;
    if (r !== undefined) {
      r.into = { ...r.into, own: put(r.into.own, m, this.root) };
    }
  }

  /** land merges what t asked for, the frames since put back over it. */
  land(t: ThreadTicket, page: readonly Message[], next = ""): void {
    if (t !== this.asked) {
      return;
    }
    this.asked = undefined;
    const r = this.reloading;
    const read = { ...(r?.into ?? this.read) };
    const mine = page.filter((m) => m.roomId === this.room);
    if (t.ask.kind === "root") {
      read.rootAsked = true;
    } else {
      read.token = next;
      read.oldest = mine.reduce((o, m) => (o === undefined || compare(m, o) < 0 ? m : o), read.oldest);
      if (r === undefined) {
        this.wantOlder = false; // a reload's page is not the one the window asked for
      }
    }
    let own: readonly Message[] = merge(read.own, mine);
    for (const m of this.since ?? []) {
      own = put(own, m, this.root);
    }
    this.since = undefined;
    read.own = own;
    if (r === undefined) {
      this.read = read;
    } else {
      r.into = read;
      this.reloaded();
    }
  }

  /**
   * failed is t refused: nothing more is read until the window asks (older). A reload's
   * is to be asked again after a wait (again), said by the answer: nothing else asks for
   * it, and until it is read what was read here may be stale.
   */
  failed(t: ThreadTicket): boolean {
    if (t !== this.asked) {
      return false;
    }
    this.asked = undefined;
    this.since = undefined;
    this.stalled = true;
    return this.reloading !== undefined;
  }

  /** again asks for a reload's failed read again: the wait after it is over. */
  again(): void {
    if (this.reloading !== undefined) {
      this.stalled = false;
    }
  }

  /**
   * reload is the stream's read_again: what was read here is read again, and shown in
   * its place once read; until then it stays shown, kept by the frames.
   */
  reload(): void {
    this.asked = undefined;
    this.since = undefined;
    this.stalled = false;
    // What is shown is still what was read before any reload: as far back is read again.
    this.reloading = {
      into: nothingRead,
      to: this.read.token === "" ? "all" : this.read.oldest,
      root: this.read.rootAsked,
    };
    this.reloaded();
  }

  /** reloaded ends the reload once nothing is due for it: what it read is shown. */
  private reloaded(): void {
    const r = this.reloading;
    if (r !== undefined && due(r) === undefined) {
      this.read = r.into;
      this.reloading = undefined;
    }
  }
}

/** due is a reload's next read: the root if it was read before, then pages as far back as before. */
function due(r: { into: Read; to: Message | "all" | undefined; root: boolean }): ThreadAsk | undefined {
  const { into, to } = r;
  if (r.root && !into.rootAsked) {
    return { kind: "root" };
  }
  if (to === undefined || into.token === "") {
    return undefined;
  }
  const short = into.oldest === undefined || to === "all" || compare(into.oldest, to) > 0;
  return short ? { kind: "page", from: into.token ?? "" } : undefined;
}

/** put is rows with m as the cache now holds it: in place of its row, or added when it is the thread's. */
function put(rows: readonly Message[], m: Message, root: string): readonly Message[] {
  const others = rows.filter((r) => r.id !== m.id);
  return others.length < rows.length || m.id === root || m.threadRoot === root ? place(others, m) : rows;
}
