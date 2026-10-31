// Spelling in the composer, as the TUI checks it (tui/spell.go, spellwalk.go,
// autocorrect.go): kithd's engine, with its dictionaries and this person's own words
// (CheckSpelling), asked on a pause in typing. Chromium's own checker is off (main.ts),
// so a word is marked once, by the same verdict the terminal gives. The marks are held
// against the text they were checked in, and moved onto the draft as it is now whoever
// changed it (the person, autocorrect, a suggestion taken, kithd folding in the
// assistant's words): a mark the change touched is dropped until checked again.
import type { Misspelling } from "../gen/backend/v1/services_pb";
import { utf16At } from "./spans";

/** The pause after the last keystroke before a check (the TUI's spellDebounce). */
export const spellDebounce = 200;
/** How many suggestions the correction walk offers (keys 1–9). */
export const walkRows = 9;

/** A word worth marking, in UTF-16 indices into the text it was checked in. */
export interface Mark {
  word: string;
  start: number;
  end: number;
  /** What the engine offered instead, best first. */
  suggestions: readonly string[];
  /** The engine accepts it and the frequency list does not: a hint, not an error. */
  rare: boolean;
  /** A rare mark kithd is sure enough of to correct unasked. */
  certain: boolean;
}

/** How this device marks and corrects words ([spell] in device.toml). */
export interface SpellSettings {
  underline: string;
  rareUnderline: string;
  autocorrect: string;
  checkBeforeSend: boolean;
  rareBeforeSend: boolean;
}

/** marksOf is kithd's answer for text, its byte offsets made UTF-16 indices. */
export function marksOf(text: string, found: readonly Misspelling[]): Mark[] {
  const at = utf16At(text);
  return found.map((f) => ({
    word: f.word,
    start: at(f.start),
    end: at(f.end),
    suggestions: f.suggestions,
    rare: f.rare,
    certain: f.certain,
  }));
}

/** endsWord: typing ch finished a word. Apostrophes and hyphens live inside words. */
export function endsWord(ch: string): boolean {
  return !/^[\p{L}\p{N}'-]/u.test(ch);
}

/** The code point before i in text, and the one at i. */
function before(text: string, i: number): string {
  const c = text.charCodeAt(i - 1);
  return c >= 0xdc00 && c <= 0xdfff && i >= 2 ? text.slice(i - 2, i) : text.slice(i - 1, i);
}

function after(text: string, i: number): string {
  return String.fromCodePoint(text.codePointAt(i) ?? 0);
}

/**
 * shiftMarks moves marks found in old onto now. The edit between them is what lies
 * between their common prefix and suffix; a mark it touched is dropped, one after it
 * moves with it, and one no longer covering a whole word is dropped (typing `l` after a
 * marked `wor` does not mark `wor` in `worl`).
 */
export function shiftMarks(marks: readonly Mark[], old: string, now: string): Mark[] {
  if (marks.length === 0) {
    return [];
  }
  let head = 0;
  const most = Math.min(old.length, now.length);
  while (head < most && old.charCodeAt(head) === now.charCodeAt(head)) {
    head++;
  }
  let tail = 0;
  while (tail < most - head && old.charCodeAt(old.length - 1 - tail) === now.charCodeAt(now.length - 1 - tail)) {
    tail++;
  }
  const editEnd = old.length - tail;
  const delta = now.length - old.length;
  const out: Mark[] = [];
  for (const m of marks) {
    if (old === now || m.end <= head) {
      out.push(m);
    } else if (m.start >= editEnd) {
      out.push({ ...m, start: m.start + delta, end: m.end + delta });
    }
  }
  return out.filter(
    (m) =>
      m.start >= 0 &&
      m.end <= now.length &&
      m.start < m.end &&
      (m.start === 0 || endsWord(before(now, m.start))) &&
      (m.end === now.length || endsWord(after(now, m.end))),
  );
}

/**
 * carried is where a caret at caret in old is in now: before the edit between them it
 * stays, after it it moves with it, and inside it it goes to the edit's end.
 */
export function carried(old: string, now: string, caret: number): number {
  let head = 0;
  const most = Math.min(old.length, now.length);
  while (head < most && old.charCodeAt(head) === now.charCodeAt(head)) {
    head++;
  }
  let tail = 0;
  while (tail < most - head && old.charCodeAt(old.length - 1 - tail) === now.charCodeAt(now.length - 1 - tail)) {
    tail++;
  }
  if (caret <= head) {
    return Math.min(caret, now.length);
  }
  return caret >= old.length - tail ? caret + now.length - old.length : now.length - tail;
}

/**
 * drawn is the marks to draw with the caret at caret: none on the word the caret is in
 * (unfinished, not misspelled), and none of a kind whose underline is "none".
 */
export function drawn(marks: readonly Mark[], caret: number, s: SpellSettings): Mark[] {
  return marks.filter(
    (m) =>
      (m.rare ? s.rareUnderline : s.underline) !== "none" &&
      // Inclusive: a caret just after the last letter is still in the word.
      !(caret >= m.start && caret <= m.end),
  );
}

/** A run of the draft as drawn under it: plain, or the word of marks[mark]. */
export interface Piece {
  text: string;
  mark: number | undefined;
}

/** pieces cuts text at the marks drawn with the caret at caret (drawn), in order. */
export function pieces(text: string, marks: readonly Mark[], caret: number, s: SpellSettings): Piece[] {
  const shown = new Set(drawn(marks, caret, s));
  const out: Piece[] = [];
  let at = 0;
  marks.forEach((m, i) => {
    if (!shown.has(m) || m.start < at || m.end > text.length) {
      return;
    }
    if (m.start > at) {
      out.push({ text: text.slice(at, m.start), mark: undefined });
    }
    out.push({ text: text.slice(m.start, m.end), mark: i });
    at = m.end;
  });
  if (at < text.length) {
    out.push({ text: text.slice(at), mark: undefined });
  }
  return out;
}

/** corrects: [spell] autocorrect may rewrite this class of word ("misspellings" or "rare"). */
export function corrects(autocorrect: string, cls: "misspellings" | "rare"): boolean {
  return autocorrect === "all" || autocorrect === cls;
}

/**
 * autocorrection is the fix for m when it is certain enough to apply unasked: for a
 * rare word kithd decides (certain, one suggestion); for a misspelling, a sole
 * suggestion, or a first one a single slip away.
 */
export function autocorrection(m: Mark, autocorrect: string): string | undefined {
  if (m.rare) {
    return corrects(autocorrect, "rare") && m.certain && m.suggestions.length === 1 ? m.suggestions[0] : undefined;
  }
  if (!corrects(autocorrect, "misspellings")) {
    return undefined;
  }
  const [first] = m.suggestions;
  if (first === undefined) {
    return undefined; // usually a name: never touched
  }
  return m.suggestions.length === 1 || oneCertainEdit(m.word, first) ? first : undefined;
}

/**
 * oneCertainEdit: want is word with one slip undone, a case change, two neighbours
 * swapped or a letter dropped. A wrong letter is not ("cet" could be cat, cot or set),
 * nor an extra one.
 */
export function oneCertainEdit(word: string, want: string): boolean {
  if (word === want) {
    return false;
  }
  if (word.toLowerCase() === want.toLowerCase()) {
    return true;
  }
  const w = Array.from(word);
  const t = Array.from(want);
  return transposed(w, t) || oneShort(w, t);
}

function transposed(w: string[], t: string[]): boolean {
  if (w.length !== t.length) {
    return false;
  }
  let i = 0;
  while (i < w.length && w[i] === t[i]) {
    i++;
  }
  return (
    i + 1 < w.length && w[i] === t[i + 1] && w[i + 1] === t[i] && w.slice(i + 2).join("") === t.slice(i + 2).join("")
  );
}

function oneShort(w: string[], t: string[]): boolean {
  if (t.length !== w.length + 1) {
    return false;
  }
  let at = 0;
  while (at < w.length && w[at] === t[at]) {
    at++;
  }
  return w.slice(at).join("") === t.slice(at + 1).join("");
}

/** The correction walk: a snapshot of the marks, stepped through one at a time. */
export interface Walk {
  items: readonly Mark[];
  at: number;
  /** A send opened it ([spell] check_before_send): reaching the end sends. */
  sendWhenDone: boolean;
}

/** What a key does while the walk is open (the TUI's [keys.spell] defaults). */
export type WalkAction =
  | { kind: "take"; n: number }
  | { kind: "add" | "ignore" | "skip" | "ignoreAll" | "skipAll" | "stop" }
  | { kind: "swallow" };

/**
 * walkAction is what key does in the walk: 1–9 take a suggestion, a adds the word, i
 * ignores it, s or space skips it, I ignores all, S skips all, esc stops. Any other key
 * that would type is swallowed (the walk owns the keys); the rest (arrows) do as usual.
 */
export function walkAction(key: string, modified: boolean): WalkAction | undefined {
  if (modified) {
    return undefined;
  }
  if (/^[1-9]$/.test(key)) {
    return { kind: "take", n: Number(key) - 1 };
  }
  const named: Record<string, WalkAction["kind"]> = {
    a: "add",
    i: "ignore",
    s: "skip",
    " ": "skip",
    I: "ignoreAll",
    S: "skipAll",
    Escape: "stop",
  };
  const kind = named[key];
  if (kind !== undefined && kind !== "take" && kind !== "swallow") {
    return { kind };
  }
  return key.length === 1 || key === "Enter" || key === "Backspace" || key === "Delete"
    ? { kind: "swallow" }
    : undefined;
}

/** withoutWord is marks less every one of word. */
export function withoutWord(marks: readonly Mark[], word: string): Mark[] {
  return marks.filter((m) => m.word !== word);
}

/** withoutLaterCopies drops the marks of word after at, so one answer covers them. */
export function withoutLaterCopies(items: readonly Mark[], at: number, word: string): Mark[] {
  return items.filter((m, i) => i <= at || m.word !== word);
}

/**
 * replaced is text with the walk's current word, and each later copy of it, put right
 * (an earlier one skipped stays), front to back: the caret goes after the last fix, the
 * later items move with each, and the copies fixed are answered. A range that no longer
 * holds its word is left alone; the current one not holding it, nothing is (caret -1).
 */
export function replaced(text: string, w: Walk, put: string): { text: string; caret: number; items: Mark[] } {
  const current = w.items[w.at];
  let items = [...w.items];
  if (current === undefined || text.slice(current.start, current.end) !== current.word) {
    return { text, caret: -1, items }; // the range no longer holds its word: nothing replaced
  }
  let delta = 0;
  let caret = -1;
  const fixed: number[] = [];
  for (let i = w.at; i < items.length; i++) {
    const m = items[i];
    if (m === undefined) {
      continue;
    }
    const start = m.start + delta;
    const end = m.end + delta;
    if ((i !== w.at && m.word !== current.word) || text.slice(start, end) !== m.word) {
      items[i] = { ...m, start, end };
      continue;
    }
    text = text.slice(0, start) + put + text.slice(end);
    caret = start + put.length;
    delta += put.length - (m.end - m.start);
    if (i !== w.at) {
      fixed.push(i);
    }
  }
  items = items.filter((_, i) => !fixed.includes(i));
  return { text, caret, items };
}

/** A check's answer: the marks, or nothing to check with (off), or no answer (failed). */
export type Checked = { marks: Mark[] } | "off" | "failed";

/** unavailable: kithd has nothing to check spelling with (FAILED_PRECONDITION, through main). */
export function unavailable(err: unknown): boolean {
  return err instanceof Error && err.message.includes("[failed_precondition]");
}

/**
 * Speller keeps the open draft's marks: it checks the draft after a pause in typing, at
 * once when a word was just finished with autocorrect on, and moves its marks across
 * every change in between. Each draft it is told of is counted (gen); an answer older
 * than one already taken is dropped, any other is moved onto the draft as it now is.
 */
export class Speller {
  /** Nothing to check with: kithd said so, so this run asks no more. */
  off = false;
  /** The draft went through the walk a send opened: a word skipped there holds up no send. */
  passed = false;
  private text = "";
  private marks: Mark[] = [];
  private gen = 0;
  private applied = 0;
  /** Where a word was just finished (-1 none): the check it arms may correct it. */
  private boundary = -1;
  private timer: ReturnType<typeof setTimeout> | undefined;

  /**
   * check asks kithd; changed says the marks changed; fix puts right a word autocorrect
   * may change (moved onto the draft as it now is); autocorrect is [spell] autocorrect.
   */
  constructor(
    private readonly check: (text: string) => Promise<readonly Misspelling[]>,
    private readonly changed: () => void,
    private readonly fix: (m: Mark, put: string) => void,
    private readonly autocorrect: () => string,
  ) {}

  /** shown is the draft's marks, as it now is. */
  get shown(): readonly Mark[] {
    return this.marks;
  }

  /** answered: the last answer taken is for the draft as it now is. */
  get answered(): boolean {
    return this.applied === this.gen;
  }

  /** reset forgets the draft: another room's is shown, and an answer about this one is dropped. */
  reset(): void {
    clearTimeout(this.timer);
    this.gen++;
    this.applied = this.gen;
    this.text = "";
    this.marks = [];
    this.boundary = -1;
    this.passed = false;
  }

  /** forget drops word's marks: it was taught to kithd, and the draft did not change. */
  forget(word: string): void {
    this.marks = withoutWord(this.marks, word);
    this.changed();
  }

  /** wordEnded says a keystroke just finished a word ending at at. */
  wordEnded(at: number): void {
    this.boundary = at;
  }

  /** follow is the draft now: the marks move onto it, and it is checked. */
  follow(text: string): void {
    if (this.off || text === this.text) {
      return;
    }
    this.marks = shiftMarks(this.marks, this.text, text);
    this.text = text;
    this.gen++;
    this.passed = false;
    const boundary = this.boundary;
    this.boundary = -1;
    clearTimeout(this.timer);
    if (text.trim() === "") {
      this.marks = [];
      return;
    }
    const gen = this.gen;
    if (boundary > 0) {
      // A word was just finished with autocorrect on: ask now, so the fix lands while
      // the word is in view.
      void this.ask(gen, text, boundary);
      return;
    }
    this.timer = setTimeout(() => {
      void this.ask(gen, text, -1);
    }, spellDebounce);
  }

  /** stop asks no more: the run ended. */
  stop(): void {
    clearTimeout(this.timer);
    this.off = true;
  }

  /** checkNow checks the draft as it is, at once, and takes the answer (a send asked). */
  checkNow(): Promise<Checked> {
    return this.ask(this.gen, this.text, -1);
  }

  private async ask(gen: number, text: string, correctAt: number): Promise<Checked> {
    let found: readonly Misspelling[];
    try {
      found = await this.check(text);
    } catch (err) {
      if (!unavailable(err)) {
        return "failed"; // not news (it runs per pause): the marks stay as true as they were
      }
      this.off = true;
      this.marks = [];
      this.changed();
      return "off";
    }
    const marks = marksOf(text, found);
    this.take(gen, text, marks, correctAt);
    return { marks };
  }

  /** take files an answer about text, unless a newer one was taken, moved onto the draft as it is. */
  private take(gen: number, text: string, marks: Mark[], correctAt: number): void {
    if (this.off || gen < this.applied) {
      return; // a newer answer was taken first, or the draft is another room's
    }
    this.applied = gen;
    this.marks = shiftMarks(marks, text, this.text);
    this.changed();
    // The word to correct is found by its end, not its word: one can be written twice.
    const m = correctAt > 0 ? marks.find((x) => x.end === correctAt) : undefined;
    const put = m === undefined ? undefined : autocorrection(m, this.autocorrect());
    const [moved] = m === undefined || put === undefined ? [] : shiftMarks([m], text, this.text);
    if (moved !== undefined && put !== undefined) {
      this.fix(moved, put);
    }
  }
}
