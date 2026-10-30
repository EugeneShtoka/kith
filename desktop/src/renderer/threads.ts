// Threads as the TUI groups them (domain.Conversations, CollapseThreads and
// ThreadMessages), held to Go by the conformance table internal/domain/testdata/threads.json:
// a thread's replies leave the main timeline for a summary under the message it hangs
// from, and its pane shows the root and the replies.
import type { Message } from "../gen/backend/v1/types_pb";
import { compareBytes, merge, nanos, place } from "./merge";

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

/**
 * OpenThread is the thread the pane shows: what the room's timeline holds of it, and
 * what it read itself (its root, ThreadPage's older replies), kept by the Messages
 * stream's frames as the timeline is. A page read before a frame is put back under it
 * when it lands, so a reply or the root edited or deleted meanwhile stays so.
 */
export class OpenThread {
  /** The thread's messages read here, beyond the room's rows. */
  private own: readonly Message[] = [];
  /** The next page's token: undefined before the newest was asked, "" once none is left. */
  private token: string | undefined;
  private wantOlder = false;
  private stalled = false;
  private rootAsked = false;
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
      this.own.filter((m) => !ids.has(m.id)),
    );
    return {
      room: this.room,
      root: this.root,
      rows: threadMessages(all, this.root),
      start: this.token === "",
      loading: this.asked !== undefined,
    };
  }

  /** ask is the next read, now in flight: the root when neither shown nor read, then pages. */
  ask(rows: readonly Message[]): ThreadTicket | undefined {
    if (this.asked !== undefined || this.stalled) {
      return undefined;
    }
    let ask: ThreadAsk | undefined;
    if (!this.rootAsked && !rows.some((m) => m.id === this.root) && !this.own.some((m) => m.id === this.root)) {
      ask = { kind: "root" };
    } else if (this.wantOlder && this.token !== "") {
      // Pages only when asked: a thread's replies are the network's to give (Matrix's
      // relations), and what the room holds of them is shown already.
      ask = { kind: "page", from: this.token ?? "" };
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
    this.own = put(this.own, m, this.root);
  }

  /** land merges what t asked for, the frames since put back over it. */
  land(t: ThreadTicket, page: readonly Message[], next = ""): void {
    if (t !== this.asked) {
      return;
    }
    this.asked = undefined;
    if (t.ask.kind === "root") {
      this.rootAsked = true;
    } else {
      this.token = next;
      this.wantOlder = false;
    }
    let own: readonly Message[] = merge(
      this.own,
      page.filter((m) => m.roomId === this.room),
    );
    for (const m of this.since ?? []) {
      own = put(own, m, this.root);
    }
    this.since = undefined;
    this.own = own;
  }

  /** failed is t refused: nothing more is read until the window asks (older). */
  failed(t: ThreadTicket): void {
    if (t !== this.asked) {
      return;
    }
    this.asked = undefined;
    this.since = undefined;
    this.stalled = true;
  }

  /** reload is the stream's read_again: what was read here is read again. */
  reload(): void {
    this.own = [];
    this.token = undefined;
    this.rootAsked = false;
    this.asked = undefined;
    this.since = undefined;
    this.stalled = false;
  }
}

/** put is rows with m as the cache now holds it: in place of its row, or added when it is the thread's. */
function put(rows: readonly Message[], m: Message, root: string): readonly Message[] {
  const others = rows.filter((r) => r.id !== m.id);
  return others.length < rows.length || m.id === root || m.threadRoot === root ? place(others, m) : rows;
}
