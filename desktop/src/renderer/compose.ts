// The open room's composer as the TUI's works it: send (Markdown rendered unless
// [composer] markdown is off), reply, correct and delete your own, react, mention, send
// files, tell the room you are typing, and ask before anything goes out as SMS. What is
// written is a draft (drafts.ts), kept in kithd.
import { create } from "@bufbuild/protobuf";

import { BackendService } from "../gen/backend/v1/services_pb";
import {
  type Draft,
  DraftSchema,
  type Member,
  MentionSchema,
  type Message,
  type Reaction,
  type Room,
} from "../gen/backend/v1/types_pb";
import type { DeviceSettings } from "../gen/device/v1/device_pb";
import type { Offered } from "../shared/ipc";
import { blank, type Composition, Drafts } from "./drafts";
import { emojiSet, type EmojiKind, type EmojiSet, finished, fold, noScores, palette, type Scores } from "./emoji";
import { call } from "./kith";
import { rankMembers } from "./mentions";

const backend = BackendService.method;

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

/** The question before a send to a chat that sends as SMS: what is waiting for the answer. */
export interface Asking {
  room: string;
  files: number;
}

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

  constructor(private readonly around: Surroundings) {
    this.drafts = new Drafts(
      () => {
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
  }

  /** opened is a room being opened: its emoji ranking is read, and typing elsewhere ends. */
  opened(room: string): void {
    if (this.typing.room !== room) {
      this.stopTyping();
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
    this.drafts.set(room, { ...c, body: done?.text ?? text });
    this.noteTyping(room, done?.text ?? text);
    return done?.caret ?? caret;
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
   * first ([composer] confirm_sms), the composer kept meanwhile.
   */
  async send(room: string): Promise<void> {
    const c = this.drafts.get(room);
    const files = this.offered.get(room) ?? [];
    if (c.body.trim() === "" && files.length === 0 && c.editing === "") {
      return;
    }
    const where = this.around.room(room);
    if (where?.replacement) {
      throw new Error("this room was replaced; the room that continues it takes messages");
    }
    if (where?.readOnly) {
      throw new Error("this room takes no messages from you");
    }
    if (where?.sms && (this.around.settings()?.confirmSms ?? true)) {
      this.asking = { room, files: files.length };
      this.around.changed();
      return;
    }
    await this.sendNow(room);
  }

  /** answer is the reply to the SMS question: yes sends, no keeps the composer as it is. */
  async answer(yes: boolean): Promise<void> {
    const asking = this.asking;
    this.asking = undefined;
    this.around.changed();
    if (yes && asking !== undefined) {
      await this.sendNow(asking.room);
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

  /** sendNow sends with nothing left to ask. The composer empties at once (written at once). */
  private async sendNow(room: string): Promise<void> {
    const c = this.drafts.get(room);
    const files = this.offered.get(room) ?? [];
    this.offered.delete(room);
    this.drafts.set(room, c.editing !== "" ? { ...blank, body: c.editSaved, replyTo: c.replyTo } : blank);
    this.stopTyping();
    const body = c.body.trim();
    if (files.length > 0 && c.editing === "") {
      await this.sendFiles(room, files, body);
      return;
    }
    const draft = create(DraftSchema, {
      body,
      mentions: [...c.mentions],
      replyTo: c.editing === "" ? c.replyTo : "",
      // A reply goes where its message is; anything else into the thread open, if any.
      threadRoot: c.replyTo === "" && c.editing === "" ? this.around.thread(room) || c.threadRoot : c.threadRoot,
      edits: c.editing,
      plain: !(this.around.settings()?.markdown ?? true),
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
