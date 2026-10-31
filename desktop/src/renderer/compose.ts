// The open room's composer as the TUI's works it: send (Markdown rendered unless
// [composer] markdown is off), reply, correct and delete your own, react, vote in a poll
// or ask one, mention, send files, tell the room you are typing, and ask before anything
// goes out as SMS. What is written is a draft (drafts.ts), kept in kithd.
import { create } from "@bufbuild/protobuf";

import { BackendService, type ScheduledMessage, ScheduledMessageSchema } from "../gen/backend/v1/services_pb";
import {
  type Draft,
  DraftSchema,
  type Member,
  MentionSchema,
  type Message,
  type Reaction,
  type Room,
} from "../gen/backend/v1/types_pb";
import { type DeviceSettings, DeviceService } from "../gen/device/v1/device_pb";
import type { Offered } from "../shared/ipc";
import { asked, Assist, type CompleteSettings, firstWord, type Offer, Phrases, takePhrase, takeWord } from "./complete";
import { blank, type Composition, Drafts } from "./drafts";
import {
  emojiSet,
  type EmojiKind,
  type EmojiSet,
  finished,
  fold,
  noScores,
  palette,
  type Scores,
  shortcodeAt,
} from "./emoji";
import { call } from "./kith";
import { mentionAt, rankMembers } from "./mentions";
import type { Asked, VotersRead } from "./polls";
import { queued, type Row, rows, whenOf, type When, parseSchedule } from "./schedule";
import { firstLine, pollOf, typed, type Typed, unescaped, type UserCommand } from "./slash";
import {
  corrects,
  endsWord,
  type Mark,
  replaced,
  type SpellSettings,
  Speller,
  type Walk,
  walkRows,
  withoutLaterCopies,
} from "./spell";

const backend = BackendService.method;
const device = DeviceService.method;

/** How often stored drafts are read again (the TUI's poll), and when the window is shown. */
export const draftPoll = 60_000;
/** How long the homeserver keeps a typing notice, and how often it is told again (Element's). */
export const typingTimeout = 15_000;
export const typingRefresh = 10_000;

/** A send that failed: sent again as it was, its transaction ID kept, so it goes out once. */
export interface Unsent {
  room: string;
  draft: Draft;
  error: string;
}

/** A message whose Schedule failed: queued again as it was, its transaction ID kept, so once. */
export interface Unscheduled {
  room: string;
  message: ScheduledMessage;
  error: string;
}

/** What a command sends instead of the composer's words: /me's, /plain's. */
interface Out {
  body: string;
  emote: boolean;
  plain: boolean;
}

/** A message to send later: when (as typed, read again when it is queued) and what. */
interface Later {
  when: When;
  text: string;
  body: string;
}

/**
 * The question before a send to a chat that sends as SMS: what is waiting for the
 * answer, the composer's words, a command's (out) or a message to send later.
 */
export interface Asking {
  room: string;
  files: number;
  out?: Out;
  later?: Later;
}

/** The send queue as shown: one room's ("" every room's), each row with where it goes. */
export interface QueueView {
  room: string;
  rows: readonly (Row & { where: string })[];
  /** Read at least once since shown. */
  read: boolean;
}

/** How often the queue shown is read again, and how long past a message's time to read it. */
export const queueRefresh = 15_000;
export const queueAfterDue = 1000;

/** What the window draws of the open room's composer. */
export interface ComposerView {
  draft: Composition;
  offered: readonly Offered[];
  asking: Asking | undefined;
  unsent: readonly Unsent[];
  /** The room's people for mentions, ranked; empty until first asked for. */
  members: readonly Member[];
  /** The emoji offered, and in what order: the quick palette first. */
  emoji: { set: EmojiSet; tone: string; scores: Scores; statics: readonly string[]; palette: readonly string[] };
  /** Who is this person: the messages to correct or delete, the reactions to take back. */
  selves: ReadonlySet<string>;
  /** Sends are Markdown ([composer] markdown): only then is the formatting bar offered. */
  markdown: boolean;
  /** Who chose what in the poll whose voters are shown, if one's are. */
  voters: VotersRead | undefined;
  /** The draft's spelling: its marks as it now is, how they are drawn, and the correction walk if open. */
  spell: { marks: readonly Mark[]; settings: SpellSettings; walk: Walk | undefined };
  /** What completion offers for the draft, the caret at its end. */
  assist: Offer;
  /** What the composer says of itself (a check before a send, a word not added); "" nothing. */
  note: string;
  /** Messages whose Schedule failed, to queue again or dismiss. */
  unscheduled: readonly Unscheduled[];
  /** The send queue shown, if it is. */
  queue: QueueView | undefined;
  /** The commands folder's commands, for the `/` list; empty until read. */
  commands: readonly UserCommand[];
  /** The command running ("" none); the links one asks to be given one of; output to read. */
  running: string;
  choosing: { name: string; links: readonly string[] } | undefined;
  reading: { title: string; body: string; truncated: boolean } | undefined;
}

/** spellSettings is [spell] as the device's settings hold it, the TUI's defaults before they are read. */
function spellSettings(s: DeviceSettings | undefined): SpellSettings {
  return {
    underline: s?.spellUnderline ?? "curly",
    rareUnderline: s?.rareUnderline ?? "dotted",
    autocorrect: s?.autocorrect ?? "off",
    checkBeforeSend: s?.checkBeforeSend ?? false,
    rareBeforeSend: s?.rareBeforeSend ?? false,
  };
}

/** completeSettings is [complete] as the device's settings hold it. */
function completeSettings(s: DeviceSettings | undefined): CompleteSettings {
  return {
    complete: s?.complete ?? true,
    ghost: s?.completeGhost ?? true,
    ratio: Number(s?.completeRatio ?? 3),
    model: !(s?.modelDeclined ?? false),
  };
}

/** What the composer reads of the window around it. */
export interface Surroundings {
  changed(): void;
  failed(err: unknown): void;
  room(id: string): Room | undefined;
  /** The device settings the open room was opened with; undefined until read. */
  settings(): DeviceSettings | undefined;
  /** Who spoke in room lately, newest first: the first people a mention offers. */
  speakers(room: string): string[];
  /** The thread open beside room ("" none): a message answering nothing goes into it. */
  thread(room: string): string;
  /** The open room's messages as shown: what the room says next after two words. */
  messages(room: string): readonly Message[];
  /** The rooms of room's place on the rail, room among them: where its words weigh more. */
  spaceRooms(room: string): string[];
  /** The commands' own doings in the window: a search of the open room, the file dialog, a room opened. */
  search(terms: string): void;
  pick(): void;
  /** go opens room if the profile has it joined, answering whether it did. */
  go(room: string): boolean;
}

/** One run's composer: its drafts, files, failed sends and what it read for them. */
export class Composer {
  readonly drafts: Drafts;
  private readonly offered = new Map<string, Offered[]>();
  private unsent: Unsent[] = [];
  private asking: Asking | undefined;
  private readonly members = new Map<string, Member[]>();
  private readonly scores = new Map<string, Scores>();
  private selves: ReadonlySet<string> = new Set();
  private typing = { room: "", at: 0 };
  private readonly poll: ReturnType<typeof setInterval>;
  private emojiFor: { settings: DeviceSettings | undefined; set: EmojiSet } | undefined;
  private voters: VotersRead | undefined;
  /** The open room's draft's spelling and completions, and the caret in it as last told. */
  private open = "";
  private caret = 0;
  private readonly speller: Speller;
  private readonly assist: Assist;
  private walk: Walk | undefined;
  private phrases: { rows: readonly Message[]; selves: ReadonlySet<string>; index: Phrases } | undefined;
  private note = "";
  /** Messages whose Schedule failed; the send queue shown, its reads counted, its next read. */
  private unscheduled: Unscheduled[] = [];
  private queue: { room: string; entries: readonly ScheduledMessage[]; read: boolean } | undefined;
  private queueReads = 0;
  private queueAgain: ReturnType<typeof setTimeout> | undefined;
  /** The commands folder's commands by room; the one running; a link asked for; output to read. */
  private readonly commands = new Map<string, UserCommand[]>();
  private running = "";
  private choosing: { room: string; name: string; event: string; links: readonly string[] } | undefined;
  private reading: { title: string; body: string; truncated: boolean } | undefined;

  constructor(private readonly around: Surroundings) {
    this.speller = new Speller(
      async (text) => (await call(backend.checkSpelling, { text })).misspellings,
      () => {
        around.changed();
      },
      (m, put) => {
        this.fix(m, put);
      },
      () => spellSettings(around.settings()).autocorrect,
    );
    this.assist = new Assist(
      {
        complete: async (prefix) => {
          const s = around.settings();
          const r = await call(backend.completeWord, {
            prefix,
            roomIds: [this.open],
            spaceRoomIds: around.spaceRooms(this.open),
            scope: s?.completeScope ?? "room",
            sources: [...(s?.completeSources ?? [])],
            limit: BigInt(asked),
          });
          return r.candidates.map((c) => ({ word: c.word, score: Number(c.score) }));
        },
        model: async (draft) => {
          const c = this.drafts.get(this.open);
          const r = await call(backend.modelTask, {
            task: "complete",
            roomId: this.open,
            draft,
            replyTo: c.replyTo || around.thread(this.open), // what the draft answers
          });
          return { suggestion: r.text.trim(), options: r.options, refusal: r.refusal, model: r.model };
        },
      },
      () => {
        around.changed();
      },
    );
    this.drafts = new Drafts(
      () => {
        this.follow();
        around.changed();
      },
      (err) => {
        if (err !== undefined) {
          around.failed(err);
        }
      },
    );
    this.poll = setInterval(() => void this.drafts.load(), draftPoll);
  }

  /** start reads the stored drafts and who this person is: the window has its seat. */
  start(): void {
    void this.drafts.load();
    call(backend.selves, {}).then(
      (r) => {
        this.selves = new Set(r.ids);
        this.around.changed();
      },
      (err: unknown) => {
        this.around.failed(err);
      },
    );
  }

  /** stop writes nothing more and tells the room typing stopped: the run ended. */
  stop(): void {
    clearInterval(this.poll);
    this.drafts.stop();
    this.stopTyping();
    this.speller.stop();
    this.assist.stop();
    clearTimeout(this.queueAgain);
    this.queue = undefined;
  }

  /**
   * opened is a room being opened: its emoji ranking is read, typing elsewhere ends, and
   * its draft is checked and completed from now (what was asked about another's is dropped).
   */
  opened(room: string): void {
    if (this.typing.room !== room) {
      this.stopTyping();
    }
    if (room !== this.open) {
      this.open = room;
      this.caret = this.drafts.get(room).body.length;
      this.walk = undefined;
      this.note = "";
      this.speller.reset();
      this.assist.reset();
      this.follow();
    }
    const scope = this.around.settings()?.reactionsScope ?? "room";
    if (scope === "static" || this.scores.has(room)) {
      return;
    }
    this.scores.set(room, noScores);
    for (const kind of ["reaction", "composed"] as const) {
      call(backend.emojiScores, { kind, roomId: room, scope }).then(
        (r) => {
          const now = this.scores.get(room) ?? noScores;
          this.scores.set(room, { ...now, [kind]: fold(r.scores) });
          this.around.changed();
        },
        () => undefined, // unranked, the palette is the static one
      );
    }
  }

  view(room: string): ComposerView {
    const settings = this.around.settings();
    let made = this.emojiFor;
    if (made === undefined || made.settings !== settings) {
      made = { settings, set: emojiSet(settings?.emojiSet ?? "curated", settings?.emojiExtra ?? {}) };
      this.emojiFor = made;
    }
    const set = made.set;
    const tone = settings?.skinTone ?? "";
    const statics = settings?.reactionsStatic ?? [];
    const scores = settings?.reactionsScope === "static" ? noScores : (this.scores.get(room) ?? noScores);
    return {
      draft: this.drafts.get(room),
      offered: this.offered.get(room) ?? [],
      asking: this.asking?.room === room ? this.asking : undefined,
      unsent: this.unsent.filter((u) => u.room === room),
      members: this.members.get(room) ?? [],
      emoji: { set, tone, scores, statics, palette: palette(set, scores, statics, tone) },
      selves: this.selves,
      markdown: settings?.markdown ?? true,
      voters: this.voters?.room === room ? this.voters : undefined,
      spell: {
        marks: room === this.open ? this.speller.shown : [],
        settings: spellSettings(settings),
        walk: room === this.open ? this.walk : undefined,
      },
      assist:
        room === this.open && this.walk === undefined
          ? this.assist.offer(this.drafts.get(room).body, completeSettings(settings), () => this.phrasesOf(room))
          : { ghost: "", from: "", lead: "", words: [], options: [], marker: "" },
      note: room === this.open ? this.note : "",
      unscheduled: this.unscheduled.filter((u) => u.room === room),
      queue:
        this.queue === undefined
          ? undefined
          : {
              room: this.queue.room,
              read: this.queue.read,
              rows: rows(this.queue.entries, this.queue.room, new Date()).map((r) => ({
                ...r,
                where: this.around.room(r.room)?.name ?? r.room,
              })),
            },
      commands: this.commands.get(room) ?? [],
      running: this.running,
      choosing: this.choosing?.room === room ? this.choosing : undefined,
      reading: this.reading,
    };
  }

  /**
   * type is the person changing room's words, caret where it is: a `:shortcode:` just
   * closed becomes its emoji. It answers where the caret goes.
   */
  type(room: string, text: string, caret: number): number {
    const c = this.drafts.get(room);
    const view = this.view(room);
    const done = finished(view.emoji.set, text, caret, view.emoji.tone);
    if (done !== undefined) {
      this.record(room, "composed", done.emoji);
    }
    if (room === this.open) {
      this.caret = done?.caret ?? caret;
      this.note = "";
      this.wordEnded(c.body, text, caret);
    }
    this.drafts.set(room, { ...c, body: done?.text ?? text });
    this.noteTyping(room, done?.text ?? text);
    return done?.caret ?? caret;
  }

  /** moved is the caret moved in the open room's words, nothing typed. */
  moved(caret: number): void {
    if (caret !== this.caret) {
      this.caret = caret;
      this.follow();
    }
  }

  /**
   * wordEnded tells the speller a keystroke (one character, not a paste: never corrected
   * silently) just finished a word, when autocorrect may correct it.
   */
  private wordEnded(was: string, text: string, caret: number): void {
    const ch = Array.from(text.slice(Math.max(0, caret - 2), caret)).at(-1) ?? "";
    const at = caret - ch.length;
    const autocorrect = spellSettings(this.around.settings()).autocorrect;
    const one = ch !== "" && text.length === was.length + ch.length && text.slice(0, at) + text.slice(caret) === was;
    if (one && at > 0 && endsWord(ch) && (corrects(autocorrect, "misspellings") || corrects(autocorrect, "rare"))) {
      this.speller.wordEnded(at);
    }
  }

  /**
   * follow keeps the open draft's spelling and completions with it, after any change:
   * typed here, a correction or a completion taken, kithd folding in the assistant's words.
   */
  private follow(): void {
    if (this.open === "") {
      return;
    }
    const text = this.drafts.get(this.open).body;
    this.caret = Math.min(this.caret, text.length);
    this.speller.follow(text);
    const quiet =
      this.walk !== undefined ||
      mentionAt(text, this.caret) !== undefined ||
      shortcodeAt(text, this.caret) !== undefined;
    this.assist.follow(text, this.caret, completeSettings(this.around.settings()), quiet);
  }

  /** phrasesOf is room's next-word index, built again only when its messages or who is me changed. */
  private phrasesOf(room: string): Phrases {
    const rows = this.around.messages(room);
    if (this.phrases?.rows !== rows || this.phrases.selves !== this.selves) {
      this.phrases = { rows, selves: this.selves, index: Phrases.of(rows, this.selves) };
    }
    return this.phrases.index;
  }

  /** fix puts right what autocorrect may change: one edit, the caret moving with it. */
  private fix(m: Mark, put: string): void {
    const c = this.drafts.get(this.open);
    if (c.body.slice(m.start, m.end) !== m.word) {
      return; // edited meanwhile
    }
    this.drafts.set(this.open, { ...c, body: c.body.slice(0, m.start) + put + c.body.slice(m.end) });
  }

  /**
   * complete takes what completion offers: right takes the word being finished or a word
   * of the next ones, Tab all of it. It answers the caret, or undefined when nothing was
   * on offer, so the key keeps its usual meaning.
   */
  complete(room: string, all: boolean): number | undefined {
    const text = this.drafts.get(room).body;
    const offer = this.view(room).assist;
    switch (offer.from) {
      case "word":
        return this.put(room, takeWord(text, offer.lead));
      case "model":
        return this.put(room, takePhrase(text, all ? offer.ghost : firstWord(offer.ghost)));
      case "phrase":
        return this.put(room, takePhrase(text, offer.ghost));
      case "":
        return undefined;
    }
  }

  /** choose takes the n-th (from 0) on offer: the finished words mid-word, the next words at its end. */
  choose(room: string, n: number): number | undefined {
    const text = this.drafts.get(room).body;
    const offer = this.view(room).assist;
    if (offer.words.length > 0) {
      const w = offer.words[n];
      return w === undefined ? undefined : this.put(room, takeWord(text, w.word));
    }
    const option = offer.options[n];
    return option === undefined ? undefined : this.put(room, takePhrase(text, option));
  }

  private put(room: string, taken: { text: string; caret: number } | undefined): number | undefined {
    if (taken === undefined) {
      return undefined;
    }
    this.caret = taken.caret;
    this.drafts.set(room, { ...this.drafts.get(room), body: taken.text });
    return taken.caret;
  }

  /**
   * openWalk starts the correction walk over the open draft's marks, at the mark at (the
   * one clicked), or the first. It says why when there is nothing to walk.
   */
  openWalk(at = 0): void {
    const marks = this.speller.shown;
    if (this.speller.off) {
      this.say("nothing to check spelling with");
    } else if (this.drafts.get(this.open).body.trim() === "") {
      this.say("nothing written to check");
    } else if (marks.length === 0) {
      this.say("nothing misspelled");
    } else {
      this.walk = { items: [...marks], at: Math.min(Math.max(at, 0), marks.length - 1), sendWhenDone: false };
      this.around.changed();
    }
  }

  /** walkTake takes the current word's n-th suggestion (from 0), for it and its later copies. */
  walkTake(n: number): number | undefined {
    const w = this.walk;
    const m = w?.items[w.at];
    const put = n < walkRows ? m?.suggestions[n] : undefined;
    if (w === undefined || put === undefined) {
      return undefined;
    }
    const c = this.drafts.get(this.open);
    const r = replaced(c.body, w, put);
    this.walk = { ...w, items: r.items };
    if (r.caret >= 0) {
      this.caret = r.caret;
      this.drafts.set(this.open, { ...c, body: r.text });
    }
    void this.walkNext();
    return r.caret >= 0 ? r.caret : undefined;
  }

  /**
   * walkLearn teaches kithd the current word: for good into the personal dictionary
   * (a rare word, past the frequency rule instead), or for this session (ignore). Its
   * marks go at once: the draft did not change, so no check would take them away.
   */
  walkLearn(forever: boolean): void {
    const w = this.walk;
    const m = w?.items[w.at];
    if (w === undefined || m === undefined) {
      return;
    }
    this.speller.forget(m.word);
    this.walk = { ...w, items: withoutLaterCopies(w.items, w.at, m.word) };
    this.learn(m.word, forever, m.rare);
    void this.walkNext();
  }

  /** walkSkip leaves the current word and its later copies as written, still marked. */
  walkSkip(): void {
    const w = this.walk;
    const m = w?.items[w.at];
    if (w !== undefined && m !== undefined) {
      this.walk = { ...w, items: withoutLaterCopies(w.items, w.at, m.word) };
    }
    void this.walkNext();
  }

  /** walkIgnoreAll accepts every word not yet answered, for this session, and ends the walk. */
  walkIgnoreAll(): void {
    const w = this.walk;
    if (w === undefined) {
      return;
    }
    for (const word of new Set(w.items.slice(w.at).map((m) => m.word))) {
      this.speller.forget(word);
      this.learn(word, false, false);
    }
    void this.walkEnd();
  }

  /** walkEnd ends the walk as answered: one a send opened sends, the rest still marked. */
  async walkEnd(): Promise<void> {
    const sending = this.walk?.sendWhenDone ?? false;
    this.walk = undefined;
    this.around.changed();
    if (sending) {
      this.speller.passed = true; // a word skipped, still marked, opens no walk again
      await this.send(this.open);
    }
  }

  /** walkStop ends the walk, a send it holds up with it, leaving what it did not reach marked. */
  walkStop(): void {
    this.walk = undefined;
    this.around.changed();
  }

  private walkNext(): Promise<void> {
    const w = this.walk;
    if (w !== undefined && w.at + 1 < w.items.length) {
      this.walk = { ...w, at: w.at + 1 };
      this.around.changed();
      return Promise.resolve();
    }
    return this.walkEnd();
  }

  private learn(word: string, forever: boolean, rare: boolean): void {
    const asked = rare && forever ? call(backend.allowRareWord, { word }) : call(backend.learnWord, { word, forever });
    asked.catch(() => {
      this.say(`"${word}" could not be added to the dictionary`);
    });
  }

  private say(note: string): void {
    this.note = note;
    this.around.changed();
  }

  /**
   * spellGate is [spell] check_before_send: with a word marked, the walk opens instead,
   * and the send goes on at its end. A draft changed since its last check is checked
   * first; one changed while it was, waits to be sent again. It answers whether to send now.
   */
  private async spellGate(room: string): Promise<boolean> {
    const s = spellSettings(this.around.settings());
    if (
      !s.checkBeforeSend ||
      room !== this.open ||
      this.speller.off ||
      this.speller.passed ||
      this.walk !== undefined
    ) {
      return true;
    }
    const text = this.drafts.get(room).body;
    let marks = this.speller.shown;
    if (!this.speller.answered) {
      this.say("checking spelling…");
      const checked = await this.speller.checkNow();
      if (this.drafts.get(room).body !== text || this.open !== room) {
        this.say("the draft changed while it was being checked — send again");
        return false;
      }
      this.note = "";
      if (checked === "off" || checked === "failed") {
        return true; // nothing to check with, or no answer: the send goes
      }
      marks = this.speller.shown;
    }
    if (!s.rareBeforeSend) {
      marks = marks.filter((m) => !m.rare); // a hint holds up no send unless asked to
    }
    if (marks.length === 0) {
      return true;
    }
    this.walk = { items: [...marks], at: 0, sendWhenDone: true };
    this.around.changed();
    return false;
  }

  /** mention puts m's name in place of text[start, end) and the mention with the draft. */
  mention(room: string, m: Member, start: number, end: number): number {
    const c = this.drafts.get(room);
    const name = m.displayName || m.userId;
    const body = `${c.body.slice(0, start)}${name} ${c.body.slice(end)}`;
    this.drafts.set(room, {
      ...c,
      body,
      mentions: [...c.mentions, create(MentionSchema, { userId: m.userId, name })],
    });
    return start + name.length + 1;
  }

  /** emoji records an emoji put in the words, so the ranking learns it. */
  emoji(room: string, emoji: string): void {
    this.record(room, "composed", emoji);
  }

  /** wantMembers reads the room's people for mentions, the first time they are asked for. */
  wantMembers(room: string): void {
    if (this.members.has(room)) {
      return;
    }
    this.members.set(room, []);
    call(backend.mentionCandidates, { roomId: room, limit: 0n }).then(
      (r) => {
        this.members.set(room, rankMembers(r.members, this.around.speakers(room), [], 0));
        this.around.changed();
      },
      (err: unknown) => {
        this.members.delete(room);
        this.around.failed(err);
      },
    );
  }

  reply(m: Message): void {
    const c = this.drafts.get(m.roomId);
    this.drafts.set(m.roomId, { ...c, replyTo: m.id, threadRoot: m.threadRoot });
  }

  /** edit corrects a message of this person's: the words in the composer wait till it ends. */
  edit(m: Message): void {
    const c = this.drafts.get(m.roomId);
    if (c.editing !== "") {
      return;
    }
    this.drafts.set(m.roomId, { ...c, editSaved: c.body, editing: m.id, body: m.body });
  }

  /** cancel ends a correction (the words before it come back) or drops the reply target. */
  cancel(room: string): void {
    const c = this.drafts.get(room);
    this.drafts.set(
      room,
      c.editing !== "" ? { ...c, body: c.editSaved, editing: "", editSaved: "" } : { ...c, replyTo: "" },
    );
  }

  /** remove deletes a message (Redact). */
  async remove(m: Message): Promise<void> {
    await call(backend.redact, { roomId: m.roomId, eventId: m.id, reason: "" });
  }

  /** react toggles key on m: taken back if this person reacted so, else sent. */
  async react(m: Message, key: string, reactions: readonly Reaction[]): Promise<void> {
    const mine = reactions.find((r) => r.key === key && this.selves.has(r.sender));
    if (mine !== undefined) {
      await call(backend.redact, { roomId: m.roomId, eventId: mine.id, reason: "" });
      return;
    }
    this.record(m.roomId, "reaction", key);
    await call(backend.sendReaction, { roomId: m.roomId, target: m.id, key });
  }

  /**
   * vote votes in m's poll by its answers' IDs, none taking the vote back; the results
   * come with the poll's message. Who voted, if shown, is put away (tui castVote).
   */
  async vote(m: Message, options: readonly string[]): Promise<void> {
    this.closeVoters();
    if (m.poll === undefined || m.redacted) {
      return;
    }
    if (m.poll.closed) {
      throw new Error("this poll is closed");
    }
    try {
      await call(backend.votePoll, { roomId: m.roomId, eventId: m.id, options: [...options] });
    } catch (err) {
      throw new Error(`could not vote: ${err instanceof Error ? err.message : String(err)}`, { cause: err });
    }
  }

  /** showVoters reads who chose what in m's poll, shown until put away: one poll's at a time. */
  showVoters(m: Message): void {
    const read: VotersRead = { room: m.roomId, event: m.id, read: false, answer: undefined, error: "" };
    this.voters = read;
    this.around.changed();
    const land = (answered: Partial<VotersRead>) => {
      if (this.voters === read) {
        this.voters = { ...read, read: true, ...answered };
        this.around.changed();
      }
    };
    call(backend.pollVoters, { roomId: m.roomId, eventId: m.id }).then(
      (answer) => {
        land({ answer });
      },
      (err: unknown) => {
        land({ error: err instanceof Error ? err.message : String(err) });
      },
    );
  }

  /** closeVoters puts away who voted. */
  closeVoters(): void {
    if (this.voters !== undefined) {
      this.voters = undefined;
      this.around.changed();
    }
  }

  /** ask asks a poll in room (CreatePoll); it answers whether it was asked, saying why not. */
  async ask(room: string, poll: Asked): Promise<boolean> {
    try {
      await call(backend.createPoll, { roomId: room, ...poll });
      return true;
    } catch (err) {
      this.around.failed(`could not create the poll: ${err instanceof Error ? err.message : String(err)}`);
      return false;
    }
  }

  /** offer adds files to send with room's next message. */
  offer(room: string, files: readonly Offered[]): void {
    this.offered.set(room, [...(this.offered.get(room) ?? []), ...files]);
    this.around.changed();
  }

  /** discard takes an offered file back. */
  async discard(room: string, token: string): Promise<void> {
    this.offered.set(
      room,
      (this.offered.get(room) ?? []).filter((o) => o.token !== token),
    );
    this.around.changed();
    await window.kith.discard(token);
  }

  /**
   * send sends room's composer: the files offered, the words as their caption, or the
   * words alone; a correction replaces its message. To a chat that sends as SMS it asks
   * first ([composer] confirm_sms), the composer kept meanwhile. Words that are a command
   * (slash.ts) are done instead, as the TUI's submit does: before the spelling check, and
   * never while correcting a message (its words are the message).
   */
  async send(room: string): Promise<void> {
    const c = this.drafts.get(room);
    const files = this.offered.get(room) ?? [];
    if (c.body.trim() === "" && files.length === 0 && c.editing === "") {
      return;
    }
    const t = c.editing === "" ? typed(c.body) : undefined;
    if (t !== undefined && t.kind !== "message" && !(await this.command(room, t))) {
      return;
    }
    if (!(await this.spellGate(room))) {
      return;
    }
    await this.sendOut(room, files.length);
  }

  /** sendOut sends the composer's words, or a command's (out), once nothing stands in the way. */
  private async sendOut(room: string, files: number, out?: Out): Promise<void> {
    const where = this.around.room(room);
    if (where?.replacement) {
      throw new Error("this room was replaced; the room that continues it takes messages");
    }
    if (where?.readOnly) {
      throw new Error("this room takes no messages from you");
    }
    if (where?.sms && (this.around.settings()?.confirmSms ?? true)) {
      this.asking = { room, files, out };
      this.around.changed();
      return;
    }
    await this.sendNow(room, out);
  }

  /** answer is the reply to the SMS question: yes sends, no keeps the composer as it is. */
  async answer(yes: boolean): Promise<void> {
    const asking = this.asking;
    this.asking = undefined;
    this.around.changed();
    if (!yes || asking === undefined) {
      return;
    }
    if (asking.later !== undefined) {
      await this.scheduleNow(asking.room, asking.later);
    } else {
      await this.sendNow(asking.room, asking.out);
    }
  }

  /** retry sends a failed message again, under the transaction ID it had: it goes out once. */
  async retry(u: Unsent): Promise<void> {
    await call(backend.send, { roomId: u.room, draft: u.draft });
    this.unsent = this.unsent.filter((x) => x !== u);
    this.around.changed();
  }

  /** dismiss forgets a failed send. */
  dismiss(u: Unsent): void {
    this.unsent = this.unsent.filter((x) => x !== u);
    this.around.changed();
  }

  /**
   * sent empties room's composer as a message leaves it, sent or queued (c is it as it
   * was): a correction's earlier words come back.
   */
  private sent(room: string, c: Composition): void {
    this.drafts.set(room, c.editing !== "" ? { ...blank, body: c.editSaved, replyTo: c.replyTo } : blank);
    this.stopTyping();
  }

  /**
   * sendNow sends with nothing left to ask: the composer's words and files, or a command's
   * words (out). The composer empties at once (written at once).
   */
  private async sendNow(room: string, out?: Out): Promise<void> {
    const c = this.drafts.get(room);
    const files = out === undefined ? (this.offered.get(room) ?? []) : [];
    if (out === undefined) {
      this.offered.delete(room);
    }
    this.sent(room, c);
    const body = out?.body ?? (c.editing === "" ? unescaped(c.body.trim()) : c.body.trim());
    if (files.length > 0 && c.editing === "") {
      await this.sendFiles(room, files, body);
      return;
    }
    const draft = create(DraftSchema, {
      body,
      mentions: [...c.mentions],
      replyTo: c.editing === "" ? c.replyTo : "",
      emote: out?.emote ?? false,
      // A reply goes where its message is; anything else into the thread open, if any.
      threadRoot: c.replyTo === "" && c.editing === "" ? this.around.thread(room) || c.threadRoot : c.threadRoot,
      edits: c.editing,
      plain: (out?.plain ?? false) || !(this.around.settings()?.markdown ?? true),
      // One ID per message: sent again, it is the same message (domain.Draft.TxnID).
      txnId: `kith-${crypto.randomUUID()}`,
    });
    try {
      await call(backend.send, { roomId: room, draft });
    } catch (err) {
      this.unsent = [...this.unsent, { room, draft, error: err instanceof Error ? err.message : String(err) }];
      this.around.changed();
    }
  }

  /**
   * later queues room's words to send at a time given as kind says (the Later form): the
   * TUI's /at and /in rules. Not while correcting a message: a correction is not queued.
   */
  async later(room: string, kind: When, text: string): Promise<void> {
    const c = this.drafts.get(room);
    if (c.editing !== "") {
      return;
    }
    const body = unescaped(c.body.trim());
    if (body === "") {
      this.say("nothing written to send later");
      return;
    }
    await this.scheduleOut(room, { when: kind, text: text.trim(), body });
  }

  /** scheduleOut queues a message once its time reads and nothing else stands in the way (the TUI's scheduleComposed). */
  private async scheduleOut(room: string, later: Later): Promise<void> {
    const at = whenOf(later.when, later.text, new Date());
    if (!(at instanceof Date)) {
      this.say(`/${later.when} ${at.error}`);
      return;
    }
    const where = this.around.room(room);
    if (where?.readOnly) {
      throw new Error("this room takes no messages from you");
    }
    if (where?.sms && (this.around.settings()?.confirmSms ?? true)) {
      this.asking = { room, files: 0, later };
      this.around.changed();
      return;
    }
    await this.scheduleNow(room, later);
  }

  /**
   * scheduleNow queues the message in kithd, its time read now, with the reply, thread and
   * mentions the composer holds, and empties the composer as a send does. One that is not
   * queued waits to be queued again under its transaction ID: kithd queues it once.
   */
  private async scheduleNow(room: string, later: Later): Promise<void> {
    const at = whenOf(later.when, later.text, new Date());
    if (!(at instanceof Date)) {
      this.say(`/${later.when} ${at.error}`);
      return;
    }
    const c = this.drafts.get(room);
    const message = create(ScheduledMessageSchema, {
      roomId: room,
      body: later.body,
      // A reply goes where its message is; anything else into the thread open, if any.
      threadRoot: c.replyTo === "" ? this.around.thread(room) || c.threadRoot : c.threadRoot,
      replyTo: c.replyTo,
      mentions: [...c.mentions],
      atMs: BigInt(at.getTime()),
      writtenMs: BigInt(Date.now()),
      txnId: `kith-${crypto.randomUUID()}`,
    });
    this.sent(room, c);
    await this.queueIt({ room, message, error: "" });
  }

  /** queueIt asks kithd to queue u's message, saying where it went or keeping it to ask again. */
  private async queueIt(u: Unscheduled): Promise<void> {
    try {
      await call(backend.schedule, { message: u.message });
      this.unscheduled = this.unscheduled.filter((x) => x.message.txnId !== u.message.txnId);
      const where = this.around.room(u.room)?.name ?? u.room;
      const at = new Date(Number(u.message.atMs));
      this.say(queued(at, where, u.message.threadRoot !== "", u.message.replyTo !== ""));
      if (this.queue !== undefined) {
        this.readQueue();
      }
    } catch (err) {
      const error = err instanceof Error ? err.message : String(err);
      const others = this.unscheduled.filter((x) => x.message.txnId !== u.message.txnId);
      this.unscheduled = [...others, { ...u, error }];
      this.around.changed();
    }
  }

  /** scheduleAgain queues a message whose Schedule failed, as it was: once, its answer lost or not. */
  async scheduleAgain(u: Unscheduled): Promise<void> {
    await this.queueIt(u);
  }

  /** dismissUnscheduled forgets a message whose Schedule failed. */
  dismissUnscheduled(u: Unscheduled): void {
    this.unscheduled = this.unscheduled.filter((x) => x.message.txnId !== u.message.txnId);
    this.around.changed();
  }

  /** showQueue shows the send queue, room's ("" every room's), read again while it is shown. */
  showQueue(room: string): void {
    this.queue = { room, entries: this.queue?.entries ?? [], read: false };
    this.around.changed();
    this.readQueue();
  }

  /** closeQueue puts the queue away; a read on its way is dropped. */
  closeQueue(): void {
    this.queueReads++;
    clearTimeout(this.queueAgain);
    this.queue = undefined;
    this.around.changed();
  }

  /**
   * readQueue reads the queue as kithd holds it: only the newest read is shown. While
   * shown, it is read again a little after its soonest message is due, and every
   * queueRefresh, so one sent, failing or refused meanwhile shows as it is.
   */
  private readQueue(): void {
    const mine = ++this.queueReads;
    clearTimeout(this.queueAgain);
    const again = (entries: readonly ScheduledMessage[]) => {
      const now = Date.now();
      const due = entries.map((m) => Number(m.atMs) + queueAfterDue).filter((at) => at > now);
      const next = Math.min(now + queueRefresh, ...due);
      this.queueAgain = setTimeout(() => {
        this.readQueue();
      }, next - now);
    };
    call(backend.scheduledMessages, {}).then(
      (r) => {
        if (mine === this.queueReads && this.queue !== undefined) {
          this.queue = { ...this.queue, entries: r.messages, read: true };
          again(r.messages);
          this.around.changed();
        }
      },
      (err: unknown) => {
        if (mine === this.queueReads && this.queue !== undefined) {
          this.queue = { ...this.queue, read: true };
          again(this.queue.entries);
          this.around.failed(err);
        }
      },
    );
  }

  /**
   * cancelQueued cancels a queued message. kithd says whether it was still waiting: one
   * whose time came first (or canceled elsewhere) is not said to be canceled.
   */
  async cancelQueued(id: string): Promise<void> {
    try {
      const r = await call(backend.cancelScheduled, { id });
      this.say(r.canceled ? "canceled" : "it was no longer waiting: it went out, or was canceled elsewhere");
    } finally {
      this.readQueue();
    }
  }

  /** retryQueued sends a refused or failing queued message now. */
  async retryQueued(id: string): Promise<void> {
    try {
      await call(backend.retryScheduled, { id });
      this.say("trying again");
    } finally {
      this.readQueue();
    }
  }

  /** wantCommands reads the commands folder's commands for room's `/` list, as the TUI scans it as the list opens. */
  wantCommands(room: string): void {
    void this.readCommands(room);
  }

  private async readCommands(room: string): Promise<UserCommand[] | undefined> {
    try {
      const r = await call(device.commands, { roomId: room });
      const listed = r.commands.map((c) => ({ name: c.name, applies: c.applies, places: c.places }));
      this.commands.set(room, listed);
      this.around.changed();
      return listed;
    } catch (err) {
      this.around.failed(err);
      return undefined;
    }
  }

  /**
   * command does a command typed into room's composer, answering whether its words are a
   * message after all (a slash word no command has). A command the window has not got,
   * or one missing its argument, says so and keeps the words.
   */
  private async command(room: string, t: Exclude<Typed, { kind: "message" }>): Promise<boolean> {
    if (t.kind === "user") {
      return this.userCommand(room, t.name, t.arg);
    }
    const { command, arg } = t;
    if (!command.window) {
      this.say(`${command.name} is in the TUI; it is not in the window yet`);
      return false;
    }
    if (command.arg !== "" && !command.argOptional && arg === "") {
      this.say(`${command.name} ${command.arg}`);
      return false;
    }
    const where = this.around.room(room);
    switch (command.name) {
      case "/me":
      case "/plain":
        await this.sendOut(room, 0, { body: arg, emote: command.name === "/me", plain: command.name === "/plain" });
        return false;
      case "/at":
      case "/in": {
        const kind = command.name === "/at" ? "at" : "in";
        const parsed = parseSchedule(kind, arg, new Date());
        if ("error" in parsed) {
          this.say(`/${kind} ${parsed.error}`);
          return false;
        }
        await this.scheduleOut(room, { when: kind, text: arg.trim().split(" ", 1)[0] ?? "", body: parsed.body });
        return false;
      }
      case "/poll": {
        const poll = pollOf(arg);
        if (poll === undefined) {
          this.say("/poll <question> | <answer1> | <answer2> [| …] [| any]");
          return false;
        }
        this.done(room);
        await this.ask(room, poll);
        return false;
      }
    }
    // The rest act on the room and leave nothing to send: the command's words go.
    this.done(room);
    switch (command.name) {
      case "/scheduled":
        this.showQueue(room);
        break;
      case "/search":
        this.around.search(arg);
        break;
      case "/upload":
        this.around.pick();
        break;
      case "/topic":
        if (where?.topic) {
          this.reading = { title: where.name || room, body: where.topic, truncated: false };
          this.around.changed();
        } else {
          this.say("this room has no topic");
        }
        break;
      case "/replacement":
        if (!where?.replacement) {
          this.say("this room has not been replaced");
        } else if (!this.around.go(where.replacement)) {
          this.say("the room that replaced this one is not joined here: the TUI joins it");
        }
        break;
    }
    return false;
  }

  /** done empties room's composer of a command's words, as the TUI's doneWithCommand does. */
  private done(room: string): void {
    this.drafts.set(room, { ...this.drafts.get(room), body: "", mentions: [] });
  }

  /**
   * userCommand runs a command of the commands folder (kith-relay runs it), or answers
   * that the words are a message: no command has that name. Outside its places it is
   * refused, never sent as words; and the window gives it no words of its own.
   */
  private async userCommand(room: string, name: string, arg: string): Promise<boolean> {
    const listed = await this.readCommands(room);
    if (listed === undefined) {
      return false; // the list could not be read: nothing is sent unsure
    }
    const found = listed.find((c) => c.name === name);
    if (found === undefined) {
      return true;
    }
    if (!found.applies) {
      this.say(`/${name} is only for ${found.places.join(", ")}`);
    } else if (arg !== "") {
      this.say(`/${name}: the window runs your commands with no words after them`);
    } else if (this.running !== "") {
      this.say(`/${this.running} is still running`);
    } else {
      await this.runCommand(room, name, this.drafts.get(room).replyTo, 0);
    }
    return false;
  }

  /** runCommand has the relay run name in room, on event ("" the room's newest), given its link-th link. */
  private async runCommand(room: string, name: string, event: string, link: number): Promise<void> {
    this.running = name;
    this.choosing = undefined;
    this.say(`/${name} …`);
    try {
      const r = await call(device.runCommand, { name, roomId: room, eventId: event, link });
      this.ran(room, name, event, r);
    } catch (err) {
      this.say(`/${name}: ${err instanceof Error ? err.message : String(err)}`);
    } finally {
      this.running = "";
      this.around.changed();
    }
  }

  /** ran puts a command's output where its block says, as the TUI's handleCommandRan does. */
  private ran(
    room: string,
    name: string,
    event: string,
    r: { output: string; body: string; truncated: boolean; why: string; links: readonly string[] },
  ): void {
    const said = `/${name}`;
    if (r.links.length > 0) {
      this.choosing = { room, name, event, links: r.links };
      this.say(`${said} — which link?`);
      return;
    }
    if (r.why !== "") {
      this.say(`${said}: ${r.why}`);
      return;
    }
    switch (r.output) {
      case "send":
        // Sent into the room by the relay: no reply or mentions went with it (the TUI's).
        this.drafts.set(room, { ...this.drafts.get(room), body: "", mentions: [], replyTo: "" });
        this.say(`${said}: sent`);
        return;
      case "status":
        this.done(room);
        this.say(`${said}: ${firstLine(r.body)}`);
        return;
      case "pager":
        this.done(room);
        this.reading = { title: said, body: r.body, truncated: r.truncated };
        this.say("");
        return;
      case "clipboard":
        this.done(room);
        this.say(`${said}'s output is copied`);
        return;
      case "none":
        this.done(room);
        this.say(`${said} ran`);
        return;
    }
    this.drafts.set(room, { ...this.drafts.get(room), body: r.body });
    this.say(`${said}${r.truncated ? " printed more than fits;" : ""} — enter to send`);
  }

  /** chooseLink runs the command that asked which link again, given the i-th (from 0). */
  async chooseLink(i: number): Promise<void> {
    const c = this.choosing;
    if (c !== undefined) {
      await this.runCommand(c.room, c.name, c.event, i + 1);
    }
  }

  /** closeChoosing drops the question which link, the command not run. */
  closeChoosing(): void {
    this.choosing = undefined;
    this.around.changed();
  }

  /** closeReading puts a command's output, or a topic, away. */
  closeReading(): void {
    this.reading = undefined;
    this.around.changed();
  }

  /** sendFiles sends each file, the words as the first one's caption. */
  private async sendFiles(room: string, files: readonly Offered[], caption: string): Promise<void> {
    for (const [i, f] of files.entries()) {
      try {
        const sent = await window.kith.attach(f.token, room, i === 0 ? caption : "");
        if (sent.kept) {
          this.around.failed(`${f.name} went with its metadata: kith cannot remove it from this kind of picture`);
        }
      } catch (err) {
        this.around.failed(`${f.name} was not sent: ${err instanceof Error ? err.message : String(err)}`);
      }
    }
  }

  /** noteTyping tells room the person is typing, once every typingRefresh; empty words stop it. */
  private noteTyping(room: string, text: string): void {
    if (!(this.around.settings()?.sendTyping ?? false)) {
      return;
    }
    if (text.trim() === "") {
      this.stopTyping();
      return;
    }
    if (this.typing.room !== "" && this.typing.room !== room) {
      this.stopTyping();
    }
    if (this.typing.room === room && Date.now() - this.typing.at < typingRefresh) {
      return;
    }
    this.typing = { room, at: Date.now() };
    void call(backend.sendTyping, { roomId: room, typing: true, timeoutMs: BigInt(typingTimeout) }).catch(
      () => undefined, // a notice that does not arrive changes nothing
    );
  }

  private stopTyping(): void {
    const room = this.typing.room;
    if (room === "") {
      return;
    }
    this.typing = { room: "", at: 0 };
    void call(backend.sendTyping, { roomId: room, typing: false, timeoutMs: 0n }).catch(() => undefined);
  }

  private record(room: string, kind: EmojiKind, emoji: string): void {
    void call(backend.recordEmoji, { kind, roomId: room, emoji }).catch(() => undefined);
  }
}
