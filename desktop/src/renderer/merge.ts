// A room's messages in order, and a newer copy of one put in place: ported from
// domain.CompareMessages and domain.MergeMessages, held to them by the conformance table
// internal/domain/testdata/timeline.json.
import type { Formatted, Message } from "../gen/backend/v1/types_pb";

/** No time: before every other, as Go's zero time. */
const zero = -(1n << 80n);

/** nanos is a message's time in nanoseconds, zero when it has none. */
function nanos(m: Message): bigint {
  const t = m.timestamp;
  return t === undefined ? zero : t.seconds * 1_000_000_000n + BigInt(t.nanos);
}

/** millis is a field of Unix milliseconds in nanoseconds, 0 being none. */
function millis(ms: bigint): bigint {
  return ms === 0n ? zero : ms * 1_000_000n;
}

/** utf8Length is how many bytes s takes in UTF-8, as Go's len. */
function utf8Length(s: string): number {
  let n = 0;
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i);
    if (c < 0x80) {
      n += 1;
    } else if (c < 0x800) {
      n += 2;
    } else if (c >= 0xd800 && c <= 0xdbff && i + 1 < s.length) {
      n += 4; // a surrogate pair: one code point beyond the BMP
      i++;
    } else {
      n += 3;
    }
  }
  return n;
}

/**
 * compareBytes orders strings as Go's strings.Compare, byte by byte in UTF-8: code point
 * by code point, which UTF-16's code units alone would get wrong past U+D7FF.
 */
export function compareBytes(a: string, b: string): number {
  if (a === b) {
    return 0;
  }
  let i = 0;
  let j = 0;
  while (i < a.length && j < b.length) {
    const x = a.codePointAt(i) ?? 0;
    const y = b.codePointAt(j) ?? 0;
    if (x !== y) {
      return x < y ? -1 : 1;
    }
    i += x > 0xffff ? 2 : 1;
    j += y > 0xffff ? 2 : 1;
  }
  return i < a.length ? 1 : j < b.length ? -1 : 0;
}

function sign(a: bigint, b: bigint): number {
  return a === b ? 0 : a < b ? -1 : 1;
}

/**
 * compare is timeline order: time, then seq, then the shorter ID (Telegram's 99 before
 * 100), then the ID.
 */
export function compare(a: Message, b: Message): number {
  return (
    sign(nanos(a), nanos(b)) || sign(a.seq, b.seq) || utf8Length(a.id) - utf8Length(b.id) || compareBytes(a.id, b.id)
  );
}

/** editTime is when the edit a message shows was sent: editedAt, or its time on an edit event. */
function editTime(m: Message): bigint {
  if (m.editedAtUnixMs !== 0n) {
    return millis(m.editedAtUnixMs);
  }
  if (m.revisionId !== "" && m.revisionId !== m.id) {
    return nanos(m);
  }
  return zero;
}

/** supersedes: edit replaces the one shown at shownAt; the later wins, then the larger revision. */
function supersedes(edit: Message, shownAt: bigint, shownRev: string): boolean {
  const at = editTime(edit);
  if (at !== shownAt) {
    return at > shownAt;
  }
  return compareBytes(edit.revisionId, shownRev) >= 0;
}

function formatIsZero(f: Formatted | undefined): boolean {
  return f === undefined || (f.text === "" && f.spans.length === 0);
}

/** toMillis is a time in nanoseconds as the wire's Unix milliseconds. */
function toMillis(at: bigint): bigint {
  return at === zero ? 0n : at / 1_000_000n;
}

/**
 * combine folds b onto a, first seen: a deletion and an edit stick, missing fields fill,
 * the earliest time wins, a newer edit's words win, and a plain copy never takes back an
 * edit or a deletion (domain.combine).
 */
function combine(first: Message, b: Message): Message {
  const a = { ...first };
  if (b.redacted) {
    a.body = b.body;
    a.format = b.format;
    if (b.redactedBy !== "") {
      a.redactedBy = b.redactedBy;
      a.redactedReason = b.redactedReason;
    }
    if (b.redactedAtUnixMs !== 0n) {
      a.redactedAtUnixMs = b.redactedAtUnixMs;
    }
  }
  a.redacted ||= b.redacted;
  a.edited ||= b.edited;
  a.mentioned ||= b.mentioned;
  a.senderName ||= b.senderName;
  if (a.timestamp === undefined || (b.timestamp !== undefined && nanos(b) < nanos(a))) {
    a.timestamp = b.timestamp;
  }
  a.sender ||= b.sender;
  a.replyTo ||= b.replyTo;
  a.threadRoot ||= b.threadRoot; // an edit arrives with no thread
  a.media ??= b.media;
  a.poll = b.poll ?? a.poll; // a poll's later copy has the votes
  if (a.seq === 0n) {
    a.seq = b.seq;
  }
  if (a.mentions.length === 0) {
    a.mentions = b.mentions;
  }
  if (a.redacted) {
    // Never regains a body, not even from an edit.
  } else if (b.reverted) {
    a.body = b.body;
    a.format = b.format;
    a.edited = b.edited;
    a.editedAtUnixMs = b.editedAtUnixMs;
    a.revisionId = b.revisionId;
  } else if (b.edited && (!a.edited || supersedes(b, editTime(a), a.revisionId))) {
    a.body = b.body;
    a.format = b.format;
    a.editedAtUnixMs = toMillis(editTime(b));
    a.revisionId = b.revisionId;
    if (b.media !== undefined) {
      a.media = b.media; // a bridge's edit replacing its placeholder brings it
    }
    a.placeholder = false;
  } else if (a.body === "" && b.body !== "") {
    a.body = b.body;
    if (formatIsZero(a.format)) {
      a.format = b.format;
    }
  }
  if (a.redacted) {
    a.editedAtUnixMs = 0n;
    a.revisionId = "";
  }
  return a;
}

/** combineStandIn is combine, the decrypted copy never folded onto its undecrypted stand-in. */
function combineStandIn(a: Message, b: Message): Message {
  return a.undecrypted && !b.undecrypted ? combine(b, a) : combine(a, b);
}

/** merge unions two lists, one row per ID, in timeline order (domain.MergeMessages). */
export function merge(existing: readonly Message[], incoming: readonly Message[]): Message[] {
  const merged: Message[] = [];
  const index = new Map<string, number>();
  for (const group of [existing, incoming]) {
    for (const m of group) {
      const at = m.id === "" ? undefined : index.get(m.id);
      if (at !== undefined) {
        merged[at] = combineStandIn(merged[at] as Message, m);
        continue;
      }
      if (m.id !== "") {
        index.set(m.id, merged.length);
      }
      merged.push({ ...m, reverted: false }); // an instruction to combine, not kept
    }
  }
  return merged.sort(compare);
}

/**
 * place puts m into rows (in order, none with m's ID) where it sorts: what merge leaves
 * for a row merged into rows it is new to, without sorting them all again.
 */
export function place(rows: readonly Message[], m: Message): Message[] {
  let lo = 0;
  let hi = rows.length;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if (compare(rows[mid] as Message, m) < 0) {
      lo = mid + 1;
    } else {
      hi = mid;
    }
  }
  return [...rows.slice(0, lo), { ...m, reverted: false }, ...rows.slice(lo)];
}

/**
 * replace is rows with m as the cache now holds it: the row with its ID taken out and m
 * put in its place in time (tui handleIncoming), so a deleted edit takes its words back.
 */
export function replace(rows: readonly Message[], m: Message): Message[] {
  return place(
    rows.filter((r) => r.id !== m.id),
    m,
  );
}
