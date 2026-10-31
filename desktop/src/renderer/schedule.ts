// Messages sent later, as the TUI's /at and /in schedule them (internal/tui/schedule.go):
// the time rules, ported with a conformance table (internal/tui/testdata/schedule.json),
// and what the queue's rows say. kithd keeps the queue and sends from it.
import type { ScheduledMessage } from "../gen/backend/v1/services_pb";

/** How a time is given: a clock time (/at) or a delay (/in). */
export type When = "at" | "in";

/** A time read, or why it is none. */
export type Parsed = { at: Date; body: string } | { error: string };

const nanos: Record<string, number> = {
  ns: 1,
  us: 1e3,
  "µs": 1e3, // micro sign
  "μs": 1e3, // Greek mu
  ms: 1e6,
  s: 1e9,
  m: 60e9,
  h: 3600e9,
};

const digit = (c: string | undefined) => c !== undefined && c >= "0" && c <= "9";

/**
 * duration is Go's time.ParseDuration, in milliseconds: a sign, then numbers each with
 * its unit ("1h30m", ".5h", "90s"); "0" alone needs none. undefined where Go refuses.
 */
export function duration(text: string): number | undefined {
  let s = text;
  let sign = 1;
  if (s.startsWith("-") || s.startsWith("+")) {
    sign = s.startsWith("-") ? -1 : 1;
    s = s.slice(1);
  }
  if (s === "0") {
    return 0;
  }
  if (s === "") {
    return undefined;
  }
  let total = 0;
  while (s !== "") {
    if (!(s[0] === "." || digit(s[0]))) {
      return undefined;
    }
    let i = 0;
    while (digit(s[i])) {
      i++;
    }
    const whole = s.slice(0, i);
    s = s.slice(i);
    let fraction = "";
    if (s.startsWith(".")) {
      s = s.slice(1);
      let j = 0;
      while (digit(s[j])) {
        j++;
      }
      fraction = s.slice(0, j);
      s = s.slice(j);
    }
    if (whole === "" && fraction === "") {
      return undefined;
    }
    let u = 0;
    while (u < s.length && !(s[u] === "." || digit(s[u]))) {
      u++;
    }
    const unit = nanos[s.slice(0, u)];
    if (unit === undefined) {
      return undefined; // a unit missing, or one Go does not know
    }
    s = s.slice(u);
    total += Number(`${whole || "0"}.${fraction || "0"}`) * unit;
    if (total > 9223372036854775807) {
      return undefined; // past what a Go duration holds
    }
  }
  return (sign * total) / 1e6;
}

/** number reads 1 or 2 digits, as Go's getnum does unfixed, or exactly 2, fixed. */
function number(s: string, fixed: boolean): [number, string] | undefined {
  if (!digit(s[0])) {
    return undefined;
  }
  if (!digit(s[1])) {
    return fixed ? undefined : [Number(s[0]), s.slice(1)];
  }
  return [Number(s.slice(0, 2)), s.slice(2)];
}

/** clockOf reads text by one of Go's layouts the TUI tries: hours and minutes, or undefined. */
function clockOf(text: string, layout: "15:04" | "1504" | "3pm" | "3:04pm"): [number, number] | undefined {
  const twelve = layout.endsWith("pm");
  const hour = number(text, false);
  if (hour === undefined) {
    return undefined;
  }
  let [h, rest] = hour;
  let m = 0;
  if (layout === "15:04" || layout === "3:04pm") {
    if (!rest.startsWith(":")) {
      return undefined;
    }
    rest = rest.slice(1);
  }
  if (layout !== "3pm") {
    const minute = number(rest, true);
    if (minute === undefined) {
      return undefined;
    }
    [m, rest] = minute;
  }
  if (twelve) {
    const half = rest.slice(0, 2);
    if (half !== "am" && half !== "pm") {
      return undefined;
    }
    rest = rest.slice(2);
    if (h > 12) {
      return undefined;
    }
    if (half === "pm" && h < 12) {
      h += 12;
    } else if (half === "am" && h === 12) {
      h = 0;
    }
  }
  if (rest !== "" || h > 23 || m > 59) {
    return undefined;
  }
  return [h, m];
}

/** clock is the TUI's parseClock: a wall-clock time; one already past today means tomorrow. */
export function clock(when: string, now: Date): Date | undefined {
  for (const layout of ["15:04", "1504", "3pm", "3:04pm"] as const) {
    const hm = clockOf(when.toLowerCase(), layout);
    if (hm === undefined) {
      continue;
    }
    const at = new Date(now.getFullYear(), now.getMonth(), now.getDate(), hm[0], hm[1]);
    return at > now ? at : new Date(now.getFullYear(), now.getMonth(), now.getDate() + 1, hm[0], hm[1]);
  }
  return undefined;
}

/** quoted is text as Go's %q writes a plain string. */
const quoted = (text: string) => JSON.stringify(text);

/** whenOf is when kind's time text says, from now: the time, or why it is none. */
export function whenOf(kind: When, when: string, now: Date): Date | { error: string } {
  if (kind === "in") {
    const d = duration(when);
    if (d === undefined) {
      return { error: `${quoted(when)} is not a delay — try 30m, 2h, 1h30m` };
    }
    if (d <= 0) {
      return { error: `a delay has to be in the future — ${quoted(when)} is not` };
    }
    return new Date(now.getTime() + d);
  }
  return clock(when, now) ?? { error: `${quoted(when)} is not a time — try 09:00 or 9pm` };
}

/** parseSchedule is the TUI's parseScheduleTime: `/at 09:00 text` or `/in 2h text` split. */
export function parseSchedule(kind: When, arg: string, now: Date): Parsed {
  const trimmed = arg.trim();
  const space = trimmed.indexOf(" ");
  const rest = space < 0 ? "" : trimmed.slice(space + 1).trim();
  if (space < 0 || rest === "") {
    const example = kind === "in" ? "2h see you then" : "09:00 good morning";
    return { error: `needs a time and a message — try /${kind} ${example}` };
  }
  const at = whenOf(kind, trimmed.slice(0, space), now);
  return at instanceof Date ? { at, body: rest } : at;
}

/** clipped is text cut to n grapheme clusters, the last an ellipsis (domain.clipGraphemes). */
function clipped(text: string, n: number): string {
  const graphemes = [...new Intl.Segmenter(undefined, { granularity: "grapheme" }).segment(text)];
  if (graphemes.length <= n) {
    return text;
  }
  return `${graphemes
    .slice(0, n - 1)
    .map((g) => g.segment)
    .join("")}…`;
}

const day = new Intl.DateTimeFormat(undefined, { weekday: "short" });
const time = new Intl.DateTimeFormat(undefined, { timeStyle: "short" });

/** dayAndTime is a send time as the TUI's queue shows it, "Mon 09:00", in the window's clock. */
export function dayAndTime(at: Date): string {
  return `${day.format(at)} ${time.format(at)}`;
}

/** Row is one queued message as the list draws it. */
export interface Row {
  id: string;
  room: string;
  /** "Mon 09:00 — body", or "(overdue)", or "unsent since" (domain.ScheduledMessage.Summary). */
  summary: string;
  /** Why it waits: refused for good, or failing while retried; "" for neither. */
  problem: "refused" | "failing" | "";
  reason: string;
}

/** rowOf is one queued message as the TUI's list says it. */
export function rowOf(m: ScheduledMessage, now: Date): Row {
  const at = new Date(Number(m.atMs));
  const body = clipped(m.body.trim(), 48);
  const when = dayAndTime(at);
  const summary =
    m.atMs <= m.writtenMs
      ? `unsent since ${when} — ${body}`
      : at < now
        ? `${when} (overdue) — ${body}`
        : `${when} — ${body}`;
  const problem = m.refused !== "" ? "refused" : m.failing !== "" ? "failing" : "";
  return { id: m.id, room: m.roomId, summary, problem, reason: m.refused || m.failing };
}

/** rows is the queue as listed: one room's ("" every room), soonest first as kithd sends it. */
export function rows(queue: readonly ScheduledMessage[], room: string, now: Date): Row[] {
  return queue.filter((m) => room === "" || m.roomId === room).map((m) => rowOf(m, now));
}

/** empty is what the list says with nothing in it (the TUI's). */
export function empty(room: string): string {
  return room === "" ? "nothing scheduled" : "nothing scheduled in this room";
}

/** queued is the TUI's word once a message is queued: when, where, and how. */
export function queued(at: Date, where: string, inThread: boolean, reply: boolean): string {
  return `queued for ${dayAndTime(at)} — ${where}${inThread ? " (in this thread)" : ""}${reply ? ", as a reply" : ""}`;
}
