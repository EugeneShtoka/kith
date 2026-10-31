// How people and rooms are told apart at a glance: colored initials in place of an
// avatar, a sender's color, and the mark of the network a room is on. Each is derived
// from what the wire already gives, the same every time.

/** How many sender colors the palette holds (--hue-0 … in styles.css). */
export const hues = 8;

/**
 * hueOf is id's color in the palette, 0 to hues-1: a stable hash (FNV-1a over its
 * UTF-16 units), so one person or room keeps one color in every list and every run.
 */
export function hueOf(id: string): number {
  let h = 0x811c9dc5;
  for (let i = 0; i < id.length; i++) {
    h ^= id.charCodeAt(i);
    h = Math.imul(h, 0x01000193) >>> 0;
  }
  return h % hues;
}

const graphemes = new Intl.Segmenter(undefined, { granularity: "grapheme" });
const letterOrDigit = /[\p{L}\p{N}]/u;
const letter = /\p{L}/u;

/** first is the first grapheme of word that is a letter or a digit, or "". */
function first(word: string): string {
  for (const { segment } of graphemes.segment(word)) {
    if (letterOrDigit.test(segment)) {
      return segment;
    }
  }
  return "";
}

/**
 * initials stand in for a name's picture: the first letter of its first word and of its
 * last ("Ada Lovelace" AL, "@ada:example.org" A). A name with no letter at all, such as
 * a phone number, is "#"; an empty one "?".
 */
export function initials(name: string): string {
  if (!letter.test(name)) {
    return name.trim() === "" ? "?" : "#";
  }
  // A Matrix ID is named by its local part: "@ada:example.org" is ada's.
  const shown = /^@[^:\s]+:\S+$/u.test(name) ? name.slice(1, name.indexOf(":")) : name;
  const words = shown
    .split(/\s+/u)
    .map(first)
    .filter((g) => g !== "");
  const head = words[0] ?? "?";
  const tail = words.length > 1 ? (words[words.length - 1] ?? "") : "";
  return (head + tail).toLocaleUpperCase();
}

/** A network's mark: two letters, and the class its color goes by (net-<slug>). */
export interface NetworkMark {
  abbr: string;
  slug: string;
}

/** The networks kith speaks, by the name the wire gives them (domain.Protocol). */
const marks: Record<string, NetworkMark> = {
  Matrix: { abbr: "MX", slug: "matrix" },
  WhatsApp: { abbr: "WA", slug: "whatsapp" },
  Telegram: { abbr: "TG", slug: "telegram" },
  Signal: { abbr: "SG", slug: "signal" },
  Slack: { abbr: "SL", slug: "slack" },
  Discord: { abbr: "DC", slug: "discord" },
  Messenger: { abbr: "FB", slug: "messenger" },
  Instagram: { abbr: "IG", slug: "instagram" },
  LinkedIn: { abbr: "IN", slug: "linkedin" },
  "Google Messages": { abbr: "SMS", slug: "gmessages" },
  "Google Chat": { abbr: "GC", slug: "gchat" },
  iMessage: { abbr: "iM", slug: "imessage" },
  Twitter: { abbr: "X", slug: "twitter" },
};

/**
 * networkMark is the mark of a room's network (Room.network), or undefined where the
 * wire names none. A network not listed is marked by its first two letters.
 */
export function networkMark(network: string): NetworkMark | undefined {
  if (network === "") {
    return undefined;
  }
  const abbr = Array.from(graphemes.segment(network), (g) => g.segment)
    .slice(0, 2)
    .join("")
    .toUpperCase();
  return marks[network] ?? { abbr, slug: "other" };
}
