// The emoji the window offers and in what order, as the TUI offers them (tui/emoji.go,
// emojiorder.go, skintone.go): the tables are internal/emoji's, generated; the order is
// kithd's ranking (EmojiScores) over the static palette; [display] skin_tone applies.
import { curated, sequences, standard } from "../gen/emoji";

/** The quick palette's size, and its emoji without [display.reactions] static (the TUI's). */
export const paletteSize = 10;
export const defaultReactions = ["👍", "❤️", "😂", "🎉", "😮", "😢", "🙏", "👀", "🔥", "💯"];

/** EmojiScores' kinds: reacting and composing are separate vocabularies. */
export type EmojiKind = "reaction" | "composed";

/** An emoji set: each shortcode's emoji, each emoji's display name, and every emoji by it. */
export interface EmojiSet {
  byName: ReadonlyMap<string, string>;
  nameOf: ReadonlyMap<string, string>;
  /** Every shortcode, sorted; every distinct emoji, ordered by its display name. */
  names: readonly string[];
  all: readonly string[];
}

/**
 * emojiSet is the vocabulary of a tier ([display.emoji] set) with the person's additions:
 * curated, then standard (every single-glyph emoji), then complete (sequences and flags
 * too). Curated names win every collision, and the person's win over those.
 */
export function emojiSet(tier: string, extra: Readonly<Record<string, string>>): EmojiSet {
  const byName = new Map<string, string>();
  const add = (rows: Iterable<readonly [string, string]>) => {
    for (const [name, emoji] of rows) {
      if (name !== "" && emoji !== "") {
        byName.set(name, emoji);
      }
    }
  };
  if (tier === "complete") {
    add(sequences);
  }
  if (tier === "complete" || tier === "standard") {
    add(standard);
  }
  add(curated);
  add(Object.entries(extra));
  // Each emoji's display name: the shortest, then the first alphabetically.
  const nameOf = new Map<string, string>();
  for (const [name, emoji] of byName) {
    const best = nameOf.get(emoji);
    if (best === undefined || name.length < best.length || (name.length === best.length && name < best)) {
      nameOf.set(emoji, name);
    }
  }
  const names = [...byName.keys()].sort();
  const all = names.filter((n) => nameOf.get(byName.get(n) ?? "") === n).map((n) => byName.get(n) ?? "");
  return { byName, nameOf, names, all };
}

// The emoji that take a skin tone, narrowed to what kith offers (tui/skintone.go): the
// standard library knows no emoji properties, and a wrong entry draws the tone apart.
const toneBases = new Set(
  "☝ ✊ ✋ ✌ ✍ 🏃 👂 👇 👈 👉 👊 👋 👌 👍 👎 👏 👶 💃 💪 🕺 🖕 🖖 🙌 🙏 🚶 🤏 🤙 🤝 🤞 🤦 🤷 🫶".split(" "),
);
const modifiers = /[\u{1F3FB}-\u{1F3FF}]/gu;
const presentation = "️";

/** bare is an emoji without its tone and presentation selector: what a tone is applied to. */
export function bare(emoji: string): string {
  return emoji.replace(modifiers, "").replaceAll(presentation, "");
}

/** toned applies tone (a modifier, "" none) to emoji when it takes one, replacing any it has. */
export function toned(emoji: string, tone: string): string {
  if (tone === "" || !toneBases.has(bare(emoji))) {
    return emoji;
  }
  return bare(emoji) + tone;
}

/** shortcodeOf is an emoji's display name, under either spelling and whatever its tone. */
export function shortcodeOf(set: EmojiSet, emoji: string): string {
  const untoned = emoji.replace(modifiers, "");
  return set.nameOf.get(untoned) ?? set.nameOf.get(bare(emoji) + presentation) ?? set.nameOf.get(bare(emoji)) ?? "";
}

/** Scores is kithd's ranking of one room per kind (EmojiScores), keyed on the bare emoji. */
export interface Scores {
  reaction: ReadonlyMap<string, number>;
  composed: ReadonlyMap<string, number>;
}

export const noScores: Scores = { reaction: new Map(), composed: new Map() };

/** fold keys a ranking on the bare emoji, summing spellings that differ in selector or tone. */
export function fold(scores: Readonly<Record<string, number>>): ReadonlyMap<string, number> {
  const folded = new Map<string, number>();
  for (const [emoji, score] of Object.entries(scores)) {
    folded.set(bare(emoji), (folded.get(bare(emoji)) ?? 0) + score);
  }
  return folded;
}

/**
 * ranked orders emoji as the TUI's emojiRanker does: this kind's score, the other kind's,
 * then the place in the static palette; ties keep the order given (alphabetical).
 */
export function ranked(
  emoji: readonly string[],
  kind: EmojiKind,
  scores: Scores,
  statics: readonly string[],
): string[] {
  const primary = kind === "reaction" ? scores.reaction : scores.composed;
  const secondary = kind === "reaction" ? scores.composed : scores.reaction;
  const defaults = new Map(statics.map((e, i) => [bare(e), statics.length - i]));
  const key = (e: string): [number, number, number] => [
    primary.get(bare(e)) ?? 0,
    secondary.get(bare(e)) ?? 0,
    defaults.get(bare(e)) ?? 0,
  ];
  return emoji
    .map((e, i) => ({ e, i, k: key(e) }))
    .sort((a, b) => b.k[0] - a.k[0] || b.k[1] - a.k[1] || b.k[2] - a.k[2] || a.i - b.i)
    .map((x) => x.e);
}

/** looksLikeEmoji: no ASCII letter and no colon, which a failed shortcode or a word has. */
function looksLikeEmoji(s: string): boolean {
  return s !== "" && !/[:A-Za-z]/.test(s);
}

/** palette is the quick palette: the head of the reaction order, toned. */
export function palette(set: EmojiSet, scores: Scores, statics: readonly string[], tone: string): string[] {
  const from = statics.length > 0 ? statics : defaultReactions;
  return ranked(set.all, "reaction", scores, from.slice(0, paletteSize))
    .filter(looksLikeEmoji)
    .slice(0, paletteSize)
    .map((e) => toned(e, tone));
}

/**
 * shortcodeAt is the `:name` being typed before caret (two letters at least, as the
 * TUI's popup), or undefined: where it starts and what is typed of it.
 */
export function shortcodeAt(text: string, caret: number): { start: number; query: string } | undefined {
  const match = /(^|[\s(])(:[a-z0-9_+-]{2,})$/i.exec(text.slice(0, caret));
  if (match === null) {
    return undefined;
  }
  const typed = match[2] ?? "";
  return { start: caret - typed.length, query: typed.slice(1).toLowerCase() };
}

/** completions are the emoji whose shortcode starts with query, then contains it, ranked. */
export function completions(set: EmojiSet, query: string, scores: Scores, statics: readonly string[]): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const pass of [(n: string) => n.startsWith(query), (n: string) => n.includes(query)]) {
    for (const name of set.names) {
      const emoji = set.byName.get(name);
      if (emoji !== undefined && pass(name) && !seen.has(emoji)) {
        seen.add(emoji);
        out.push(emoji);
      }
    }
  }
  return ranked(out, "composed", scores, statics);
}

/**
 * finished replaces a `:name:` just closed before caret with its emoji, toned, as the
 * TUI does; an unknown name stays as typed.
 */
export function finished(
  set: EmojiSet,
  text: string,
  caret: number,
  tone: string,
): { text: string; caret: number; emoji: string } | undefined {
  const match = /(^|[\s(]):([a-z0-9_+-]+):$/i.exec(text.slice(0, caret));
  const emoji = match === null ? undefined : set.byName.get((match[2] ?? "").toLowerCase());
  if (match === null || emoji === undefined) {
    return undefined;
  }
  const start = caret - (match[2] ?? "").length - 2;
  const put = toned(emoji, tone);
  return { text: text.slice(0, start) + put + text.slice(caret), caret: start + put.length, emoji: put };
}
