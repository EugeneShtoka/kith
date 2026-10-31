// Completion in the composer, as the TUI offers it (tui/assist.go, domain/complete.go,
// domain/phrases.go): the word being typed finished from what has been said
// (CompleteWord), and at a word's end the next words, from the local completion model
// (ModelTask "complete", which only the model on this machine answers) and from this
// room's own messages. Nothing is put in until asked; a suggestion is drawn as ghost text
// only when it clearly leads. Each answer is kept with the word or draft it answers, and
// is shown only while that is still what is written.
import type { Message } from "../gen/backend/v1/types_pb";

/** The wait before a word is looked up (the TUI's assistDebounce). */
export const assistDebounce = 120;
/** How much of a word must be typed before anything is offered (domain.CompleteMinPrefix). */
export const minPrefix = 3;
/** How many letters a completion must save (domain.CompleteMinSaving). */
export const minSaving = 2;
/** How many completions of a word are offered (domain.CompleteChoices). */
export const choices = 3;
/** How many next words are offered at once (domain.MaxCompletionOptions). */
export const maxOptions = 5;
/** How many candidates are asked for: the leader's test reads the runners-up. */
export const asked = 6;
/** How many words a draft needs before the model is asked. */
export const modelMinWords = 3;
/** How often a next word must have been seen in the room to be drawn. */
const phraseLeast = 2;
/** How much one of your own uses counts against someone else's (domain.phraseMineWeight). */
const mineWeight = 3;
/** The room's share of the five next words once the model has answered. */
const ladderSlots = 1;

/** One completion, and the weight behind it. */
export interface Candidate {
  word: string;
  score: number;
}

const letter = /\p{L}/u;

/** trailingWord is the run of letters at the end of text (digits and apostrophes end it). */
export function trailingWord(text: string): string {
  let i = text.length;
  while (i > 0) {
    const c = text.charCodeAt(i - 1);
    const size = c >= 0xdc00 && c <= 0xdfff && i >= 2 ? 2 : 1;
    if (!letter.test(text.slice(i - size, i))) {
      break;
    }
    i -= size;
  }
  return text.slice(i);
}

/** wordBeingTyped is the word the caret ends, if worth finishing: the caret at the draft's end. */
export function wordBeingTyped(text: string, caret: number): string | undefined {
  if (caret !== text.length || text === "") {
    return undefined;
  }
  const word = trailingWord(text);
  return Array.from(word).length >= minPrefix ? word : undefined;
}

/** longEnough: word saves at least minSaving letters over prefix. */
export function longEnough(prefix: string, word: string): boolean {
  return Array.from(word).length >= Array.from(prefix).length + minSaving;
}

/** recaseLike gives word the typed prefix's case: all capitals (two letters or more), or a capital first. */
export function recaseLike(typed: string, word: string): string {
  if (typed === "" || word === "") {
    return word;
  }
  if (Array.from(typed).length > 1 && typed.toUpperCase() === typed && typed.toLowerCase() !== typed) {
    return word.toUpperCase();
  }
  const first = String.fromCodePoint(typed.codePointAt(0) ?? 0);
  if (first.toUpperCase() !== first || first.toLowerCase() === first) {
    return word;
  }
  const head = String.fromCodePoint(word.codePointAt(0) ?? 0);
  return head.toUpperCase() + word.slice(head.length);
}

/** dominant is the first candidate when it outweighs the second ratio times. */
export function dominant(candidates: readonly Candidate[], ratio: number): Candidate | undefined {
  const [lead, second] = candidates;
  if (lead === undefined || ratio < 1) {
    return undefined;
  }
  return second === undefined || lead.score >= second.score * ratio ? lead : undefined;
}

/**
 * offered is an answer applied to what was typed: re-cased, a word that does not go on
 * from it (or saves too little) dropped, at most choices. The ghost, the list and alt+N
 * all read it, so they agree on positions.
 */
export function offered(candidates: readonly Candidate[], typed: string): Candidate[] {
  const out: Candidate[] = [];
  for (const c of candidates) {
    const word = recaseLike(typed, c.word);
    if (word.startsWith(typed) && longEnough(typed, word)) {
      out.push({ word, score: c.score });
      if (out.length === choices) {
        break;
      }
    }
  }
  return out;
}

/** draftForModel is the draft to ask the next word about: caret at its end, enough words, a space last. */
export function draftForModel(text: string, caret: number): string | undefined {
  if (caret !== text.length || !text.endsWith(" ")) {
    return undefined;
  }
  return text.split(/\s+/).filter((w) => w !== "").length >= modelMinWords ? text : undefined;
}

/** sameAsk: two drafts are the same question (trailing blanks aside, so a held space asks once). */
export function sameAsk(a: string, b: string): boolean {
  return a.replace(/[ \t]+$/, "") === b.replace(/[ \t]+$/, "");
}

/** merge is first then second, case-insensitively once each, at most maxOptions. */
export function merge(first: readonly string[], second: readonly string[]): string[] {
  const out: string[] = [];
  const seen = new Set<string>();
  for (const option of [...first, ...second]) {
    if (out.length >= maxOptions) {
      break;
    }
    if (!seen.has(option.toLowerCase())) {
      seen.add(option.toLowerCase());
      out.push(option);
    }
  }
  return out;
}

/** phraseWords splits text into lower-cased runs of letters. */
function phraseWords(text: string): string[] {
  return (text.toLowerCase().match(/\p{L}+/gu) ?? []).map((w) => w);
}

/** phraseContext is a draft's last two words, when it ends in a space after two. */
export function phraseContext(draft: string): [string, string] | undefined {
  if (!draft.endsWith(" ")) {
    return undefined;
  }
  const words = phraseWords(draft);
  const first = words.at(-2);
  const second = words.at(-1);
  return first === undefined || second === undefined ? undefined : [first, second];
}

/** rank orders continuations by weight, then alphabetically, so a list is stable. */
function rank(counts: ReadonlyMap<string, number> | undefined): string[] {
  return [...(counts ?? new Map<string, number>())]
    .sort(([a, x], [b, y]) => (x !== y ? y - x : a < b ? -1 : a > b ? 1 : 0))
    .map(([word]) => word);
}

function count(index: Map<string, Map<string, number>>, key: string, word: string, weight: number): void {
  const next = index.get(key) ?? new Map<string, number>();
  index.set(key, next);
  next.set(word, (next.get(word) ?? 0) + weight);
}

/** Phrases is a room's next-word index: what followed each pair of words, and each word. */
export class Phrases {
  private readonly after = new Map<string, Map<string, number>>();
  private readonly afterOne = new Map<string, Map<string, number>>();
  private readonly words = new Map<string, number>();

  /** of indexes messages, weighting what me (every ID that is this person) sent. */
  static of(messages: readonly Message[], me: ReadonlySet<string>): Phrases {
    const p = new Phrases();
    for (const m of messages) {
      if (m.redacted || m.body === "") {
        continue;
      }
      const weight = me.has(m.sender) ? mineWeight : 1;
      const words = phraseWords(m.body);
      words.forEach((word, at) => {
        p.words.set(word, (p.words.get(word) ?? 0) + weight);
        const one = words[at - 1];
        const two = words[at - 2];
        if (one !== undefined) {
          count(p.afterOne, one, word, weight);
        }
        if (one !== undefined && two !== undefined) {
          count(p.after, `${two} ${one}`, word, weight);
        }
      });
    }
    return p;
  }

  /** next is the word that usually follows first and second, when it leads by ratio, seen at least least times. */
  next(first: string, second: string, ratio: number): string | undefined {
    const counts = this.after.get(`${first.toLowerCase()} ${second.toLowerCase()}`);
    if (counts === undefined || ratio < 1) {
      return undefined;
    }
    const [lead, runnerUp] = rank(counts);
    const leadCount = lead === undefined ? 0 : (counts.get(lead) ?? 0);
    const runnerCount = runnerUp === undefined ? 0 : (counts.get(runnerUp) ?? 0);
    if (lead === undefined || leadCount < phraseLeast || (runnerCount > 0 && leadCount < runnerCount * ratio)) {
      return undefined;
    }
    return lead;
  }

  /** nextWords is what to offer after draft, best first, at most n: never the word just typed. */
  nextWords(draft: string, n: number): string[] {
    const words = phraseWords(draft);
    const last = words.at(-1);
    if (n <= 0 || last === undefined || !draft.endsWith(" ")) {
      return [];
    }
    const out: string[] = [];
    const seen = new Set<string>([last]);
    const take = (counts: ReadonlyMap<string, number> | undefined) => {
      for (const word of rank(counts)) {
        if (out.length === n) {
          return;
        }
        if (!seen.has(word)) {
          seen.add(word);
          out.push(word);
        }
      }
    };
    const before = words.at(-2);
    if (before !== undefined) {
      take(this.after.get(`${before} ${last}`));
    }
    take(this.afterOne.get(last));
    take(this.words);
    return out;
  }
}

/** The model's answer about a draft, or why there is none. */
export interface ModelAnswer {
  /** The first option, drawn as the ghost; "" none. */
  suggestion: string;
  options: readonly string[];
  /** Why nothing was asked ("" it was). */
  refusal: string;
  model: string;
}

/** What Assist asks kithd. */
export interface AssistCalls {
  /** complete is CompleteWord for a prefix, folded to lower case. */
  complete(prefix: string): Promise<Candidate[]>;
  /** model is ModelTask "complete" for a draft. */
  model(draft: string): Promise<ModelAnswer>;
}

/** What is on offer for the draft as written, the caret at its end. */
export interface Offer {
  /** What is drawn after the caret ("" nothing), and whose it is: right takes a word of it, Tab all. */
  ghost: string;
  from: "word" | "model" | "phrase" | "";
  /** The word being typed, finished, when one clearly leads ("" none). */
  lead: string;
  /** The word being typed, finished: the list Tab opens and alt+N takes from. */
  words: readonly Candidate[];
  /** The next words at a word's end, two or more drawn as a strip. */
  options: readonly string[];
  /** The model's marker: its name while it answers, "not ready", "failing", or "". */
  marker: string;
}

/** How this device completes ([complete] in device.toml). */
export interface CompleteSettings {
  complete: boolean;
  ghost: boolean;
  ratio: number;
  /** The completion model may be asked (not [complete.model] declined). */
  model: boolean;
}

/**
 * Assist keeps the open draft's completions: a word being typed is looked up after a
 * pause, a draft at a word's end is asked of the model at once. Each ask is counted; an
 * answer is taken only for the ask still current, so one about a word or draft no longer
 * written is never offered.
 */
export class Assist {
  private word = { prefix: "", typed: "", candidates: [] as Candidate[], gen: 0 };
  private model = { text: "", answer: undefined as ModelAnswer | undefined, gen: 0, failure: "", live: false };
  private timer: ReturnType<typeof setTimeout> | undefined;

  constructor(
    private readonly calls: AssistCalls,
    private readonly changed: () => void,
  ) {}

  /** reset forgets the draft's completions: another room's draft is shown. */
  reset(): void {
    clearTimeout(this.timer);
    this.word = { prefix: "", typed: "", candidates: [], gen: this.word.gen + 1 };
    this.model = { text: "", answer: undefined, gen: this.model.gen + 1, failure: "", live: false };
  }

  stop(): void {
    this.reset();
  }

  /**
   * follow is the draft now, the caret at caret; quiet when nothing is to be asked (off,
   * a mention or emoji being picked, the correction walk open).
   */
  follow(text: string, caret: number, s: CompleteSettings, quiet: boolean): void {
    const typed = quiet || !s.complete ? undefined : wordBeingTyped(text, caret);
    if (typed === undefined) {
      if (this.word.prefix !== "") {
        clearTimeout(this.timer);
        this.word = { prefix: "", typed: "", candidates: [], gen: this.word.gen + 1 };
      }
    } else if (typed.toLowerCase() !== this.word.prefix) {
      const gen = this.word.gen + 1;
      const prefix = typed.toLowerCase();
      this.word = { prefix, typed, candidates: [], gen };
      clearTimeout(this.timer);
      this.timer = setTimeout(() => {
        this.calls.complete(prefix).then(
          (candidates) => {
            if (gen === this.word.gen) {
              this.word = { ...this.word, candidates };
              this.changed();
            }
          },
          () => undefined, // unasked for, so a failure is not news
        );
      }, assistDebounce);
    }
    const draft = quiet || !s.complete || !s.model ? undefined : draftForModel(text, caret);
    if (draft === undefined) {
      if (this.model.text !== "") {
        this.model = { ...this.model, text: "", answer: undefined, gen: this.model.gen + 1 };
      }
      return;
    }
    if (sameAsk(draft, this.model.text)) {
      return;
    }
    const gen = this.model.gen + 1;
    this.model = { ...this.model, text: draft, answer: undefined, gen };
    this.calls.model(draft).then(
      (answer) => {
        if (gen === this.model.gen) {
          this.model = { ...this.model, answer, failure: "", live: answer.refusal === "" };
          this.changed();
        }
      },
      (err: unknown) => {
        // Kept, unlike other unasked failures: a wrong model looks like the feature is off.
        if (gen === this.model.gen) {
          this.model = { ...this.model, failure: err instanceof Error ? err.message : String(err), live: false };
          this.changed();
        }
      },
    );
  }

  /** offer is what is on offer for text, written with the caret at its end. */
  offer(text: string, s: CompleteSettings, phrases: () => Phrases): Offer {
    const marker =
      this.model.failure !== ""
        ? "failing"
        : this.model.live
          ? (this.model.answer?.model ?? "")
          : this.model.answer !== undefined && this.model.answer.refusal !== ""
            ? "not ready"
            : "";
    const none: Offer = { ghost: "", from: "", lead: "", words: [], options: [], marker: s.model ? marker : "" };
    if (!s.complete) {
      return none;
    }
    const typed = wordBeingTyped(text, text.length);
    if (typed !== undefined) {
      const words = typed === this.word.typed ? offered(this.word.candidates, typed) : [];
      const lead = dominant(words, s.ratio);
      // The leader is taken by right and Tab even with the ghost off, as the TUI's keys do.
      return lead === undefined
        ? { ...none, words }
        : { ...none, words, lead: lead.word, ghost: s.ghost ? lead.word.slice(typed.length) : "", from: "word" };
    }
    const draft = draftForModel(text, text.length);
    if (draft === undefined) {
      return none;
    }
    const answer = sameAsk(draft, this.model.text) ? this.model.answer : undefined;
    const room = phrases();
    const options =
      answer === undefined
        ? room.nextWords(draft, maxOptions - 1)
        : merge(answer.options, room.nextWords(draft, ladderSlots));
    const context = phraseContext(draft);
    const next = context === undefined ? undefined : room.next(context[0], context[1], s.ratio);
    if (s.ghost && answer !== undefined && answer.suggestion !== "") {
      return { ...none, options, ghost: answer.suggestion, from: "model" };
    }
    return s.ghost && next !== undefined
      ? { ...none, options, ghost: `${next} `, from: "phrase" }
      : { ...none, options };
  }
}

/** takeWord is text with the word being typed replaced by word and a space: the caret after it. */
export function takeWord(text: string, word: string): { text: string; caret: number } | undefined {
  const typed = wordBeingTyped(text, text.length);
  if (typed === undefined || !word.startsWith(typed)) {
    return undefined;
  }
  const head = `${text.slice(0, text.length - typed.length)}${word} `;
  return { text: head, caret: head.length };
}

/** takePhrase is text with phrase after it, and a space unless it ends in punctuation. */
export function takePhrase(text: string, phrase: string): { text: string; caret: number } | undefined {
  if (phrase === "" || draftForModel(text, text.length) === undefined) {
    return undefined;
  }
  const head = text + phrase + (/[\p{L}\p{N}]$/u.test(phrase) ? " " : "");
  return { text: head, caret: head.length };
}

/** firstWord is a phrase's first word, with the blank after it. */
export function firstWord(phrase: string): string {
  const cut = phrase.search(/\s/);
  return cut > 0 ? phrase.slice(0, cut + 1) : phrase;
}

/** caretInRTL: the last strong character before the caret is right-to-left, so the arrows swap. */
export function caretInRTL(text: string, caret: number): boolean {
  for (const ch of Array.from(text.slice(0, caret)).reverse()) {
    if (/[\p{Script=Hebrew}\p{Script=Arabic}\p{Script=Syriac}\p{Script=Thaana}\p{Script=Nko}]/u.test(ch)) {
      return true;
    }
    if (/\p{L}/u.test(ch)) {
      return false;
    }
  }
  return false;
}
