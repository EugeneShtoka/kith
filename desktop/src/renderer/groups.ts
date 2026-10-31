// How the timeline is broken up for reading: a divider where a new day starts, and a
// sender's messages in a run drawn as one group (their name and face once).
import { timestampDate } from "@bufbuild/protobuf/wkt";

import type { Message } from "../gen/backend/v1/types_pb";

/** How long after a sender's message their next still joins its group. */
export const groupGap = 5 * 60_000;

/** dateOf is when m was sent, or undefined for a message with no time. */
function dateOf(m: Message): Date | undefined {
  return m.timestamp ? timestampDate(m.timestamp) : undefined;
}

/** sameDay: a and b fall on one calendar day, where the window runs. */
export function sameDay(a: Date, b: Date): boolean {
  return a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate();
}

/**
 * newDay: m starts a day, so a divider goes above it: it is the first row shown, or
 * the row before it was sent another day. A message with no time starts none.
 */
export function newDay(before: Message | undefined, m: Message): boolean {
  const at = dateOf(m);
  if (at === undefined) {
    return false;
  }
  const was = before === undefined ? undefined : dateOf(before);
  return was === undefined || !sameDay(was, at);
}

/**
 * continues: m is drawn in before's group, without its sender's name and face: one
 * sender, the same day, within groupGap, and nothing drawn between them (the unread
 * line starts a group of its own).
 */
export function continues(before: Message | undefined, m: Message, lineBetween: boolean): boolean {
  if (before === undefined || lineBetween || before.sender !== m.sender || m.sender === "") {
    return false;
  }
  const was = dateOf(before);
  const at = dateOf(m);
  if (was === undefined || at === undefined || !sameDay(was, at)) {
    return false;
  }
  const gap = at.getTime() - was.getTime();
  return gap >= 0 && gap <= groupGap;
}

const longDay = new Intl.DateTimeFormat(undefined, { weekday: "long", month: "long", day: "numeric" });
const yearDay = new Intl.DateTimeFormat(undefined, { dateStyle: "full" });

/** dayLabel names a divider's day: Today, Yesterday, the weekday and date, the year when not this one. */
export function dayLabel(day: Date, now: Date): string {
  if (sameDay(day, now)) {
    return "Today";
  }
  const yesterday = new Date(now.getFullYear(), now.getMonth(), now.getDate() - 1);
  if (sameDay(day, yesterday)) {
    return "Yesterday";
  }
  return day.getFullYear() === now.getFullYear() ? longDay.format(day) : yearDay.format(day);
}
