// A typed search, read as the TUI reads it (domain.ParseSearch): from:, since: and until:
// lifted out, the rest the terms kithd matches. Held to Go by the conformance table
// internal/domain/testdata/search.json.

/** A search as typed, taken apart. */
export interface SearchFilter {
  terms: string;
  /** An ID, or a fragment of a display name. */
  sender: string;
  /** A phonebook field term after from: (from:role:attorney): the window has no phonebook. */
  senderField: string;
  /** Since is inclusive, until exclusive; undefined: none. */
  since: Date | undefined;
  until: Date | undefined;
}

/** The ID prefixes of the networks kith reaches directly (domain natives). */
const networks = [
  "matrix",
  "whatsapp",
  "slack",
  "messenger",
  "linkedin",
  "gmessages",
  "telegram",
  "instagram",
  "signal",
];

/** isUserID: s has the shape of a person's ID on any network (domain.IsUserID). */
function isUserID(s: string): boolean {
  if (s.startsWith("matrix:")) {
    const native = s.slice("matrix:".length);
    return native.startsWith("@") && native.includes(":");
  }
  const at = s.indexOf(":");
  if (at < 0 || !networks.includes(s.slice(0, at))) {
    return false;
  }
  const rest = s.slice(at + 1);
  return rest !== "" && !rest.includes("/");
}

/** typedUserID: a bare MXID is the Matrix person; anything else is taken as written. */
function typedUserID(s: string): string {
  const t = s.trim();
  return t.startsWith("@") && t.includes(":") ? `matrix:${t}` : t;
}

/** fieldTerm: a from: value is a phonebook field term (key:value), not an ID. */
function fieldTerm(value: string): boolean {
  const at = value.indexOf(":");
  return at > 0 && at < value.length - 1 && !value.startsWith("@") && !isUserID(value);
}

/** emptyFilter: nothing to match and nothing to narrow by. */
export function emptyFilter(f: SearchFilter): boolean {
  return f.terms === "" && f.sender === "" && f.senderField === "" && f.since === undefined && f.until === undefined;
}

/** parseSearch lifts the filters out of input; the rest are terms. */
export function parseSearch(input: string, now: Date): SearchFilter {
  const filter: SearchFilter = { terms: "", sender: "", senderField: "", since: undefined, until: undefined };
  const terms: string[] = [];
  for (const field of input.split(/\s+/).filter((f) => f !== "")) {
    const at = field.indexOf(":");
    const name = at < 0 ? "" : field.slice(0, at).toLowerCase();
    const value = at < 0 ? "" : field.slice(at + 1);
    if (value === "") {
      terms.push(field);
      continue;
    }
    if (name === "from") {
      if (fieldTerm(value)) {
        filter.senderField = value;
      } else if (value.includes(":")) {
        filter.sender = typedUserID(value);
      } else {
        filter.sender = value.startsWith("@") ? value.slice(1) : value;
      }
      continue;
    }
    const when = name === "since" || name === "until" ? parseWhen(value, now, name === "until") : undefined;
    if (when === undefined) {
      terms.push(field);
    } else if (name === "since") {
      filter.since = when;
    } else {
      filter.until = when;
    }
  }
  filter.terms = terms.join(" ");
  return filter;
}

/**
 * parseWhen reads a date bound, in the window's time zone: a word (today, yesterday), a
 * date (2026-08-01), or a span counted back from now (2h, 7d, 2w, 3m, 1y). A day's bound
 * at its end is the next day's start.
 */
function parseWhen(value: string, now: Date, endOfDay: boolean): Date | undefined {
  const day = (t: Date) => new Date(t.getFullYear(), t.getMonth(), t.getDate() + (endOfDay ? 1 : 0));
  switch (value.toLowerCase()) {
    case "today":
      return day(now);
    case "yesterday":
      return day(new Date(now.getFullYear(), now.getMonth(), now.getDate() - 1));
  }
  const date = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value);
  if (date !== null) {
    const [y, m, d] = date.slice(1).map(Number) as [number, number, number];
    const at = new Date(y, m - 1, d);
    if (at.getFullYear() === y && at.getMonth() === m - 1 && at.getDate() === d) {
      return day(at);
    }
  }
  const span = /^([+-]?\d+)([hdwmy])$/.exec(value);
  const n = Number(span?.[1]);
  if (span === null || !Number.isSafeInteger(n) || n < 0) {
    return undefined;
  }
  const back = (years: number, months: number, days: number) =>
    new Date(
      now.getFullYear() - years,
      now.getMonth() - months,
      now.getDate() - days,
      now.getHours(),
      now.getMinutes(),
      now.getSeconds(),
      now.getMilliseconds(),
    );
  switch (span[2]) {
    case "h":
      return new Date(now.getTime() - n * 3_600_000);
    case "d":
      return back(0, 0, n);
    case "w":
      return back(0, 0, 7 * n);
    case "m":
      return back(0, n, 0);
    default:
      return back(n, 0, 0);
  }
}
