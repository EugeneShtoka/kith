// A room's half-written message belongs to the room and is kept in kithd, where the TUI
// reads it too (db/drafts.go). This window writes its drafts as the seat's (one window
// per profile: main names the seat), and kith-mcp appends the assistant's words beside
// it. Each write is made over the version this window last read, and kithd rebases it
// (ReplaceDraft with rebase, domain.RebaseDraft): the assistant's words added since go
// after this window's and come back to be folded in here, so neither side loses words.
import { create } from "@bufbuild/protobuf";

import { BackendService } from "../gen/backend/v1/services_pb";
import { type StoredDraft, StoredDraftSchema } from "../gen/backend/v1/services_pb";
import type { Mention } from "../gen/backend/v1/types_pb";
import { call } from "./kith";

const backend = BackendService.method;

/** One room's composition: what enter would send, and where. */
export interface Composition {
  /** The words; while a message is being corrected, the correction. */
  body: string;
  replyTo: string;
  /** The message being corrected (""), and the draft the composer returns to after. */
  editing: string;
  editSaved: string;
  /** The thread it is written into, "" for the main timeline. */
  threadRoot: string;
  mentions: readonly Mention[];
}

export const blank: Composition = { body: "", replyTo: "", editing: "", editSaved: "", threadRoot: "", mentions: [] };

/** The debounce before a write, and the longest unbroken typing goes unsaved (the TUI's). */
export const saveDelay = 900;
export const saveEvery = 5000;
/** How many of this window's unanswered writes are remembered per room (the TUI's unsureKept). */
const unsureKept = 16;

/** empty: nothing worth keeping (StoredDraft.Empty). */
export function empty(c: Composition): boolean {
  return c.body === "" && c.replyTo === "" && c.editing === "" && c.editSaved === "";
}

/** joined puts more after text, a blank line between, as kith-mcp appends (domain.JoinDraft). */
export function joined(text: string, more: string): string {
  return more === "" ? text : text === "" ? more : `${text}\n\n${more}`;
}

function fromStored(s: StoredDraft): Composition {
  return {
    body: s.body,
    replyTo: s.replyTo,
    editing: s.editing,
    editSaved: s.editSaved,
    threadRoot: s.threadRoot,
    mentions: s.mentions,
  };
}

/** sameVersion: a and b are one stored version (none stored is undefined, or empty). */
function sameVersion(a: StoredDraft | undefined, b: StoredDraft | undefined): boolean {
  if (a === undefined || empty(fromStored(a))) {
    return b === undefined || empty(fromStored(b));
  }
  return b !== undefined && !differs(fromStored(a), b) && a.updatedUnixMs === b.updatedUnixMs;
}

/** differs: c is not the composition of the stored version base, so there is something to write. */
function differs(c: Composition, base: StoredDraft | undefined): boolean {
  if (base === undefined) {
    return !empty(c);
  }
  if (empty(c)) {
    return !empty(fromStored(base));
  }
  return (
    c.body !== base.body ||
    c.editSaved !== base.editSaved ||
    c.editing !== base.editing ||
    c.replyTo !== base.replyTo ||
    c.threadRoot !== base.threadRoot
  );
}

/**
 * Drafts keeps this window's drafts and writes them to kithd: after a pause in typing,
 * at once when one is emptied (or a poll could bring a sent draft back), and at least
 * every saveEvery while typing goes on. One write per room is on the wire at a time,
 * each over what the last one stored. What it reads (load: at start, on a poll, on
 * focus) it takes for rooms where nothing here is unsaved; elsewhere this window's
 * words stand, and the next write rebases them.
 */
export class Drafts {
  private readonly local = new Map<string, Composition>();
  /** The stored version each room's draft here is based on; none: nothing stored. */
  private readonly bases = new Map<string, StoredDraft>();
  /** Rooms with a write on the wire, and those asked to write again meanwhile. */
  private readonly inflight = new Set<string>();
  private readonly dirty = new Set<string>();
  /** Rooms whose last write failed: the next load that answers writes them again. */
  private readonly retry = new Set<string>();
  /**
   * This window's writes whose answer it never heard, per room: one may be stored. kithd
   * is told, so a stored draft that is one of them, or one with the assistant's words
   * after it, is read as this window's own and not doubled.
   */
  private readonly unsure = new Map<string, StoredDraft[]>();
  /** Counts writes started and ended, and each room's last: a load older than one is not taken. */
  private epoch = 0;
  private readonly touched = new Map<string, number>();
  /** The newest stamp given a write: stamps only go up, two writes in one millisecond apart. */
  private lastMs = 0n;
  private readonly timers = new Map<string, { timer: ReturnType<typeof setTimeout>; since: number }>();
  private failing = false;
  private closed = false;

  /**
   * changed says the drafts changed (a fold, a load); failed says writes stopped being
   * kept (err), or are kept again (undefined).
   */
  constructor(
    private readonly changed: () => void,
    private readonly failed: (err: unknown) => void,
  ) {}

  /** get is room's composition here. */
  get(room: string): Composition {
    return this.local.get(room) ?? blank;
  }

  /** held is whether room holds a draft here. */
  held(room: string): boolean {
    return this.local.has(room);
  }

  /** set is the person changing room's composition. */
  set(room: string, c: Composition): void {
    if (this.closed) {
      return;
    }
    const was = this.get(room);
    this.put(room, c);
    this.changed();
    if (empty(c) && !empty(was)) {
      this.save(room); // emptied (sent, or cleared): at once
      return;
    }
    const pending = this.timers.get(room);
    const since = pending?.since ?? Date.now();
    clearTimeout(pending?.timer);
    if (Date.now() - since >= saveEvery) {
      this.save(room);
      return;
    }
    this.timers.set(room, {
      timer: setTimeout(() => {
        this.save(room);
      }, saveDelay),
      since,
    });
  }

  /**
   * load reads every stored draft and takes those written elsewhere since, where this
   * window has nothing unsaved and no write of the room started or ended since the read
   * was asked for; a draft known here and no longer stored was deleted elsewhere. It
   * writes again what failed, kithd answering again.
   */
  async load(): Promise<void> {
    const issued = this.epoch;
    let stored: StoredDraft[];
    try {
      stored = (await call(backend.drafts, {})).drafts;
    } catch (err) {
      this.failed(err);
      return;
    }
    if (this.closed) {
      return;
    }
    const seen = new Set<string>();
    for (const s of stored) {
      seen.add(s.roomId);
      this.adopt(s.roomId, s, issued);
    }
    for (const room of [...this.bases.keys()]) {
      if (!seen.has(room)) {
        this.adopt(room, undefined, issued);
      }
    }
    for (const room of [...this.retry]) {
      this.retry.delete(room);
      this.save(room);
    }
    this.changed();
  }

  /**
   * flush writes every room's unsaved draft now and waits until none is on the wire, at
   * most most ms: the window is stepping aside, or going out of sight.
   */
  async flush(most = 2000): Promise<void> {
    for (const room of new Set([...this.local.keys(), ...this.bases.keys(), ...this.timers.keys(), ...this.retry])) {
      this.retry.delete(room);
      this.save(room);
    }
    const deadline = Date.now() + most;
    while (this.inflight.size > 0 && Date.now() < deadline) {
      await new Promise((resolve) => setTimeout(resolve, 10));
    }
  }

  /** stop writes nothing more: the window left the profile, or lost its seat. */
  stop(): void {
    this.closed = true;
    for (const { timer } of this.timers.values()) {
      clearTimeout(timer);
    }
    this.timers.clear();
  }

  private put(room: string, c: Composition): void {
    if (empty(c)) {
      this.local.delete(room);
    } else {
      this.local.set(room, c);
    }
  }

  private adopt(room: string, s: StoredDraft | undefined, issued: number): void {
    const base = this.bases.get(room);
    if (sameVersion(s, base) || !this.settled(room, issued) || differs(this.get(room), base)) {
      return; // as known; or a write may have moved it since; or unsaved here, which the next write rebases
    }
    this.put(room, s === undefined ? blank : fromStored(s));
    this.based(room, s);
  }

  /** settled: no write of room is on the wire, owed, or started or answered since issued. */
  private settled(room: string, issued: number): boolean {
    return (
      !this.inflight.has(room) &&
      !this.dirty.has(room) &&
      !this.retry.has(room) &&
      (this.touched.get(room) ?? 0) <= issued
    );
  }

  private based(room: string, s: StoredDraft | undefined): void {
    if (s === undefined || empty(fromStored(s))) {
      this.bases.delete(room);
    } else {
      this.bases.set(room, s);
    }
  }

  private touch(room: string): void {
    this.touched.set(room, ++this.epoch);
  }

  /** save writes room's draft as held now, or once the write on the wire answers. */
  private save(room: string): void {
    const pending = this.timers.get(room);
    if (pending !== undefined) {
      clearTimeout(pending.timer);
      this.timers.delete(room);
    }
    if (this.closed) {
      return;
    }
    if (this.inflight.has(room)) {
      this.dirty.add(room);
      return;
    }
    const held = this.get(room);
    const over = this.bases.get(room);
    const unanswered = this.unsure.get(room) ?? [];
    if (!differs(held, over) && unanswered.length === 0) {
      return; // nothing of this window's to add: what is stored stands
    }
    this.lastMs = BigInt(Math.max(Date.now(), Number(this.lastMs) + 1));
    const draft = create(StoredDraftSchema, {
      roomId: room,
      body: held.body,
      mentions: [...held.mentions],
      replyTo: held.replyTo,
      editing: held.editing,
      editSaved: held.editSaved,
      threadRoot: held.threadRoot,
      updatedUnixMs: this.lastMs,
    });
    // Whatever becomes of its answer, this write may be stored from now on.
    this.unsure.set(room, [...unanswered, draft].slice(-unsureKept));
    this.inflight.add(room);
    this.touch(room);
    call(backend.replaceDraft, { draft, over, rebase: true, unanswered }).then(
      (answer) => {
        this.answered(room, answer.stored, answer.added);
      },
      (err: unknown) => {
        this.unanswered(room, err);
      },
    );
  }

  /** answered: room's base moves to what kithd stored, and the words it added are folded in. */
  private answered(room: string, stored: StoredDraft | undefined, added: string): void {
    this.inflight.delete(room);
    this.touch(room);
    const owed = this.dirty.delete(room);
    this.unsure.delete(room);
    this.retry.delete(room);
    this.based(room, stored);
    if (added !== "") {
      // The assistant's words, stored after this window's: after its words here too, or
      // while a message is corrected, after the draft the composer returns to.
      const c = this.get(room);
      this.put(
        room,
        c.editing !== "" ? { ...c, editSaved: joined(c.editSaved, added) } : { ...c, body: joined(c.body, added) },
      );
    }
    if (this.failing) {
      this.failing = false;
      this.failed(undefined);
    }
    this.changed();
    if (owed || differs(this.get(room), this.bases.get(room))) {
      this.save(room);
    }
  }

  /** unanswered: the write failed, landed or not; it is written again with the next change or load. */
  private unanswered(room: string, err: unknown): void {
    this.inflight.delete(room);
    this.touch(room);
    if (!this.failing) {
      this.failing = true;
      this.failed(err);
    }
    if (this.dirty.delete(room)) {
      this.save(room);
    } else {
      this.retry.add(room);
    }
  }
}
