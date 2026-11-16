// A half-written message belongs to its place, a room's main timeline or one of its
// threads, each with a draft of its own, and is kept in kithd, where the TUI reads it too
// (db/drafts.go). Any number of windows write it, and kith-mcp appends the assistant's
// words beside them. Each write is made over the version this window last
// read, and kithd rebases it (ReplaceDraft with rebase, domain.RebaseDraft): words
// another writer added since go after this window's and come back to be folded in here,
// so no side loses words.
import { create, fromJson, type JsonValue, toJson } from "@bufbuild/protobuf";

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
  /**
   * The thread it is sent into, "" for the main timeline: its place's thread, or the
   * thread of a message it answers.
   */
  threadRoot: string;
  mentions: readonly Mention[];
}

export const blank: Composition = { body: "", replyTo: "", editing: "", editSaved: "", threadRoot: "", mentions: [] };

/**
 * Words a run of the window did not get stored: the composition, the stored version it
 * was based on, and its writes not answered, so they are written later as that run would
 * have written them (keep).
 */
export interface Kept {
  draft: Composition;
  base: StoredDraft | undefined;
  unanswered: readonly StoredDraft[];
}

/** placeOf is the key of a place: a room's main timeline (thread "") or one of its threads. */
export function placeOf(room: string, thread: string): string {
  return thread === "" ? room : `${room}\n${thread}`;
}

/** partsOf is a place's room and thread (placeOf). */
export function partsOf(place: string): [room: string, thread: string] {
  const [room = "", thread = ""] = place.split("\n");
  return [room, thread];
}

/** keptText is kept words by place as text, to outlive the window reloading (keptFrom). */
export function keptText(kept: ReadonlyMap<string, Kept>): string {
  const json = (d: StoredDraft) => toJson(StoredDraftSchema, d);
  return JSON.stringify(
    [...kept].map(([place, k]) => ({
      draft: json(
        create(StoredDraftSchema, {
          ...k.draft,
          roomId: partsOf(place)[0],
          threadRoot: partsOf(place)[1],
          mentions: [...k.draft.mentions],
        }),
      ),
      base: k.base === undefined ? null : json(k.base),
      unanswered: k.unanswered.map(json),
    })),
  );
}

/** keptFrom reads keptText's text: nothing, where it does not read. */
export function keptFrom(text: string): Map<string, Kept> {
  const kept = new Map<string, Kept>();
  try {
    for (const k of JSON.parse(text) as { draft: JsonValue; base: JsonValue; unanswered: JsonValue[] }[]) {
      const draft = fromJson(StoredDraftSchema, k.draft);
      kept.set(placeOf(draft.roomId, draft.threadRoot), {
        draft: fromStored(draft),
        base: k.base === null ? undefined : fromJson(StoredDraftSchema, k.base),
        unanswered: k.unanswered.map((d) => fromJson(StoredDraftSchema, d)),
      });
    }
  } catch {
    return new Map();
  }
  return kept;
}

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
  // The thread is the place's, which names the draft, not a part of it.
  return (
    c.body !== base.body || c.editSaved !== base.editSaved || c.editing !== base.editing || c.replyTo !== base.replyTo
  );
}

/**
 * Drafts keeps this window's drafts and writes them to kithd: after a pause in typing,
 * at once when one is emptied (or a poll could bring a sent draft back), and at least
 * every saveEvery while typing goes on. One write per place is on the wire at a time,
 * each over what the last one stored. What it reads (load: at start, on a poll, on
 * focus) it takes for places where nothing here is unsaved; elsewhere this window's
 * words stand, and the next write rebases them. Its state is by place (placeOf); the
 * composer asks by room, for the place open in it (here).
 */
export class Drafts {
  private readonly local = new Map<string, Composition>();
  /** The stored version each place's draft here is based on; none: nothing stored. */
  private readonly bases = new Map<string, StoredDraft>();
  /** Places with a write on the wire, and those asked to write again meanwhile. */
  private readonly inflight = new Set<string>();
  private readonly dirty = new Set<string>();
  /** Places whose last write failed: the next load that answers writes them again. */
  private readonly retry = new Set<string>();
  /**
   * This window's writes whose answer it never heard, per place: one may be stored. kithd
   * is told, so a stored draft that is one of them, or one with the assistant's words
   * after it, is read as this window's own and not doubled.
   */
  private readonly unsure = new Map<string, StoredDraft[]>();
  /** Counts writes started and ended, and each place's last: a load older than one is not taken. */
  private epoch = 0;
  private readonly touched = new Map<string, number>();
  /** The newest stamp given a write: stamps only go up, two writes in one millisecond apart. */
  private lastMs = 0n;
  private readonly timers = new Map<string, { timer: ReturnType<typeof setTimeout>; since: number }>();
  private failing = false;
  private closed = false;

  /**
   * changed says the drafts changed (a fold, a load); failed says writes stopped being
   * kept (err), or are kept again (undefined); added says another writer's words were
   * folded into a place's draft. thread is the thread open beside a room ("" none).
   */
  constructor(
    private readonly changed: () => void,
    private readonly failed: (err: unknown) => void,
    private readonly added: (place: string) => void,
    private readonly thread: (room: string) => string,
  ) {}

  /** here is room's place now: the thread open beside it, else its main timeline. */
  here(room: string): string {
    return placeOf(room, this.thread(room));
  }

  /** get is the composition of room's place here. */
  get(room: string): Composition {
    return this.local.get(this.here(room)) ?? blank;
  }

  /** held is whether room's place holds a draft here. */
  held(room: string): boolean {
    return this.local.has(this.here(room));
  }

  /** set is the person changing the composition of room's place. */
  set(room: string, c: Composition): void {
    this.change(this.here(room), c);
  }

  /** change is place's composition changed: written after a pause, or at once when emptied. */
  private change(place: string, c: Composition): void {
    if (this.closed) {
      return;
    }
    const was = this.local.get(place) ?? blank;
    this.put(place, c);
    this.changed();
    if (empty(c) && !empty(was)) {
      this.save(place); // emptied (sent, or cleared): at once
      return;
    }
    const pending = this.timers.get(place);
    const since = pending?.since ?? Date.now();
    clearTimeout(pending?.timer);
    if (Date.now() - since >= saveEvery) {
      this.save(place);
      return;
    }
    this.timers.set(place, {
      timer: setTimeout(() => {
        this.save(place);
      }, saveDelay),
      since,
    });
  }

  /**
   * load reads every stored draft and takes those written elsewhere since, where this
   * window has nothing unsaved and no write of the place started or ended since the read
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
      const place = placeOf(s.roomId, s.threadRoot);
      seen.add(place);
      this.adopt(place, s, issued);
    }
    for (const place of [...this.bases.keys()]) {
      if (!seen.has(place)) {
        this.adopt(place, undefined, issued);
      }
    }
    for (const place of [...this.retry]) {
      this.retry.delete(place);
      this.save(place);
    }
    this.changed();
  }

  /**
   * flush writes every place's unsaved draft now and waits until none is on the wire, a
   * write that failed tried once more, at most most ms in all: the window is leaving the
   * profile, closing, or going out of sight.
   */
  async flush(most = 2000): Promise<void> {
    const deadline = Date.now() + most;
    for (let tries = 0; tries < 2; tries++) {
      for (const place of new Set([...this.local.keys(), ...this.bases.keys(), ...this.timers.keys(), ...this.retry])) {
        this.retry.delete(place);
        this.save(place);
      }
      while (this.inflight.size > 0 && Date.now() < deadline) {
        await new Promise((resolve) => setTimeout(resolve, 10));
      }
      if (this.retry.size === 0) {
        return;
      }
    }
  }

  /**
   * unstored is every place's words here kithd is not known to hold (a write failed, or
   * was not answered), with the writes not answered: kept when the window leaves the
   * profile, for when its room is open again (keep).
   */
  unstored(): Map<string, Kept> {
    const kept = new Map<string, Kept>();
    // A place whose writes are not answered may hold in kithd what it no longer holds here
    // (a draft sent, its clear unanswered): that is unstored too.
    for (const place of new Set([...this.local.keys(), ...this.unsure.keys()])) {
      const draft = this.local.get(place) ?? blank;
      const unanswered = this.unsure.get(place) ?? [];
      if (differs(draft, this.bases.get(place)) || unanswered.length > 0) {
        kept.set(place, { draft, base: this.bases.get(place), unanswered });
      }
    }
    return kept;
  }

  /**
   * keep puts back words another run of the window did not get stored, and writes them as
   * that run would have: over the version they were based on, so what was stored since
   * (the assistant's words) is rebased after them, and with its writes not answered told
   * to kithd as this window's own, so one that landed unheard is not doubled.
   */
  keep(place: string, kept: Kept): void {
    if (this.closed) {
      return;
    }
    this.based(place, kept.base);
    this.unsure.set(place, [...(this.unsure.get(place) ?? []), ...kept.unanswered].slice(-unsureKept));
    this.change(place, kept.draft);
  }

  /** stop writes nothing more: the window left the profile. */
  stop(): void {
    this.closed = true;
    for (const { timer } of this.timers.values()) {
      clearTimeout(timer);
    }
    this.timers.clear();
  }

  private put(place: string, c: Composition): void {
    if (empty(c)) {
      this.local.delete(place);
    } else {
      this.local.set(place, c);
    }
  }

  private adopt(place: string, s: StoredDraft | undefined, issued: number): void {
    const base = this.bases.get(place);
    if (sameVersion(s, base) || !this.settled(place, issued) || differs(this.local.get(place) ?? blank, base)) {
      return; // as known; or a write may have moved it since; or unsaved here, which the next write rebases
    }
    this.put(place, s === undefined ? blank : fromStored(s));
    this.based(place, s);
  }

  /** settled: no write of place is on the wire, owed, or started or answered since issued. */
  private settled(place: string, issued: number): boolean {
    return (
      !this.inflight.has(place) &&
      !this.dirty.has(place) &&
      !this.retry.has(place) &&
      (this.touched.get(place) ?? 0) <= issued
    );
  }

  private based(place: string, s: StoredDraft | undefined): void {
    if (s === undefined || empty(fromStored(s))) {
      this.bases.delete(place);
    } else {
      this.bases.set(place, s);
    }
  }

  private touch(place: string): void {
    this.touched.set(place, ++this.epoch);
  }

  /** save writes place's draft as held now, or once the write on the wire answers. */
  private save(place: string): void {
    const pending = this.timers.get(place);
    if (pending !== undefined) {
      clearTimeout(pending.timer);
      this.timers.delete(place);
    }
    if (this.closed) {
      return;
    }
    if (this.inflight.has(place)) {
      this.dirty.add(place);
      return;
    }
    const held = this.local.get(place) ?? blank;
    const over = this.bases.get(place);
    const unanswered = this.unsure.get(place) ?? [];
    if (!differs(held, over) && unanswered.length === 0) {
      return; // nothing of this window's to add: what is stored stands
    }
    this.lastMs = BigInt(Math.max(Date.now(), Number(this.lastMs) + 1));
    const [roomId, threadRoot] = partsOf(place);
    const draft = create(StoredDraftSchema, {
      roomId,
      threadRoot,
      body: held.body,
      mentions: [...held.mentions],
      replyTo: held.replyTo,
      editing: held.editing,
      editSaved: held.editSaved,
      updatedUnixMs: this.lastMs,
    });
    // Whatever becomes of its answer, this write may be stored from now on.
    this.unsure.set(place, [...unanswered, draft].slice(-unsureKept));
    this.inflight.add(place);
    this.touch(place);
    call(backend.replaceDraft, { draft, over, rebase: true, unanswered }).then(
      (answer) => {
        this.answered(place, answer.stored, answer.added);
      },
      (err: unknown) => {
        this.unanswered(place, err);
      },
    );
  }

  /** answered: place's base moves to what kithd stored, and the words it added are folded in. */
  private answered(place: string, stored: StoredDraft | undefined, added: string): void {
    this.inflight.delete(place);
    this.touch(place);
    const owed = this.dirty.delete(place);
    this.unsure.delete(place);
    this.retry.delete(place);
    this.based(place, stored);
    if (added !== "") {
      // Another window's or the assistant's words, stored after this window's: after its
      // words here too, or while a message is corrected, after the draft the composer
      // returns to.
      const c = this.local.get(place) ?? blank;
      this.put(
        place,
        c.editing !== "" ? { ...c, editSaved: joined(c.editSaved, added) } : { ...c, body: joined(c.body, added) },
      );
      this.added(place);
    }
    if (this.failing) {
      this.failing = false;
      this.failed(undefined);
    }
    this.changed();
    if (owed || differs(this.local.get(place) ?? blank, this.bases.get(place))) {
      this.save(place);
    }
  }

  /** unanswered: the write failed, landed or not; it is written again with the next change or load. */
  private unanswered(place: string, err: unknown): void {
    this.inflight.delete(place);
    this.touch(place);
    if (!this.failing) {
      this.failing = true;
      this.failed(err);
    }
    if (this.dirty.delete(place)) {
      this.save(place);
    } else {
      this.retry.add(place);
    }
  }
}
