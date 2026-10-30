// Formatting as kithd sends it (richtext's text and spans) drawn as React elements:
// never HTML. The spans' offsets are UTF-8 bytes; a JavaScript string is indexed in
// UTF-16 units, so each offset is turned into one before the text is cut.
import type { ReactNode } from "react";

import type { Formatted, Span } from "../gen/backend/v1/types_pb";

/**
 * utf16At is, for text, the function from a UTF-8 byte offset to the UTF-16 index of
 * the same place. An offset inside a character (kithd sends none) moves to its end.
 */
export function utf16At(text: string): (byte: bigint) => number {
  const starts: number[] = []; // the byte offset each UTF-16 index begins at
  let bytes = 0;
  for (let i = 0; i < text.length; i++) {
    starts.push(bytes);
    const c = text.charCodeAt(i);
    if (c < 0x80) {
      bytes += 1;
    } else if (c < 0x800) {
      bytes += 2;
    } else if (c >= 0xd800 && c <= 0xdbff && i + 1 < text.length) {
      starts.push(bytes); // the pair's second half: the same character
      bytes += 4;
      i++;
    } else {
      bytes += 3;
    }
  }
  return (byte) => {
    const b = Number(byte);
    if (b <= 0) {
      return 0;
    }
    if (b >= bytes) {
      return text.length;
    }
    let lo = 0;
    let hi = starts.length;
    while (lo < hi) {
      const mid = (lo + hi) >> 1;
      if ((starts[mid] ?? 0) < b) {
        lo = mid + 1;
      } else {
        hi = mid;
      }
    }
    return lo; // the first index at b or after: a pair's first half, never its second
  };
}

/** wrap puts piece in the elements the spans over it ask for, the link outermost. */
function wrap(piece: ReactNode, over: readonly Span[], key: number): ReactNode {
  const has = (f: (s: Span) => boolean) => over.some(f);
  let out = piece;
  if (has((s) => s.code)) {
    out = <code>{out}</code>;
  }
  if (has((s) => s.strike)) {
    out = <s>{out}</s>;
  }
  if (has((s) => s.underline)) {
    out = <u>{out}</u>;
  }
  if (has((s) => s.italic)) {
    out = <em>{out}</em>;
  }
  if (has((s) => s.bold)) {
    out = <strong>{out}</strong>;
  }
  const spoiler = over.find((s) => s.spoiler);
  if (spoiler !== undefined) {
    out = (
      <span className="spoiler" title={spoiler.reason || "spoiler"}>
        {out}
      </span>
    );
  }
  if (has((s) => s.heading)) {
    out = <span className="heading">{out}</span>;
  }
  if (has((s) => s.quote)) {
    out = <span className="quote">{out}</span>;
  }
  // A link shows where it goes; opening one is the window's to add (kith.open).
  const link = over.find((s) => s.link);
  if (link !== undefined) {
    out = (
      <span className="link" title={link.href}>
        {out}
      </span>
    );
  }
  return <span key={key}>{out}</span>;
}

/** Formatted draws f: its text cut where any span starts or ends, each piece in its marks. */
export function FormattedText({ f }: { f: Formatted }): ReactNode {
  const at = utf16At(f.text);
  const spans = f.spans.map((s) => ({ span: s, start: at(s.start), end: at(s.end) })).filter((s) => s.start < s.end);
  const cuts = [...new Set([0, f.text.length, ...spans.flatMap((s) => [s.start, s.end])])].sort((a, b) => a - b);
  const pieces: ReactNode[] = [];
  for (let i = 0; i + 1 < cuts.length; i++) {
    const from = cuts[i] ?? 0;
    const to = cuts[i + 1] ?? 0;
    const over = spans.filter((s) => s.start <= from && s.end >= to).map((s) => s.span);
    const text = f.text.slice(from, to);
    pieces.push(over.length === 0 ? text : wrap(text, over, i));
  }
  return <>{pieces}</>;
}
