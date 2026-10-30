// A fake of what the window reaches through window.kith, for the window's tests: kithd
// (its seats, rooms, unread and invitations, per profile), the relay (profiles and the
// one remembered) and main between them (the profile every request carries, decided as
// the gate decides it; streams opened again after kithd restarts, a forced seat asked
// for again unforced). Answers and events arrive later, as over IPC.
import {
  create,
  type DescMethod,
  fromBinary,
  type MessageInitShape,
  type MessageShape,
  toBinary,
} from "@bufbuild/protobuf";

import {
  BackendService,
  type RailResponse,
  RailResponseSchema,
  RailRowKind,
  type SeatRequest,
  type StoredDraft,
  StoredDraftSchema,
} from "../../gen/backend/v1/services_pb";
import {
  type Draft,
  type Member,
  type Message,
  type Reaction,
  type Room,
  RoomSchema,
  type Unread,
  UnreadSchema,
} from "../../gen/backend/v1/types_pb";
import {
  type DeviceSettingsResponse,
  DeviceSettingsResponseSchema,
  DeviceService,
} from "../../gen/device/v1/device_pb";
import { compare } from "../merge";
import type { KithBridge, Offered, StreamEvent } from "../../shared/ipc";
import { runIn } from "../../shared/profile";

const backend = BackendService.method;
const methods = new Map<string, DescMethod>(
  [...BackendService.methods, ...DeviceService.methods].map((m) => [m.name, m]),
);

/** One stream the window opened: what it asked, in which profile, and whether it is closed. */
interface Sub {
  method: string;
  profile: string;
  request: MessageShape<DescMethod["input"]>;
  listener: (event: StreamEvent) => void;
  closed: boolean;
}

/** A profile's data in kithd. */
interface Profile {
  rooms: Room[];
  unread: Map<string, Unread>;
  invites: Room[];
  /** The cache's messages, by room, and its reactions. */
  messages: Map<string, Message[]>;
  reactions: Reaction[];
}

export function room(id: string, init: MessageInitShape<typeof RoomSchema> = {}): Room {
  return create(RoomSchema, { id, name: id, ...init });
}

export function unread(roomId: string, messages: number): Unread {
  return create(UnreadSchema, { roomId, messages: BigInt(messages), counted: true });
}

export class Kithd {
  /** The relay's: the config's profiles, and the one device.toml remembers. */
  profiles: string[] = ["Home"];
  remembered = "";
  readonly data = new Map<string, Profile>();
  /** Who sits in each profile: one of the window's Seat streams, or another window. */
  readonly seats = new Map<string, Sub | "other">();
  readonly subs: Sub[] = [];
  /** Every call, as "Method:profile". */
  readonly calls: string[] = [];
  /** Calls the window made that main would have refused: none in a profile not chosen. */
  readonly refused: string[] = [];
  /** What the window told main to count in the tray, in order. */
  readonly told: number[] = [];
  /** The relay's device settings, or "fail" for a device file that does not load. */
  settings: DeviceSettingsResponse | "fail" = create(DeviceSettingsResponseSchema, {
    sendReceipts: true,
    unreadLine: true,
  });
  /** Every receipt sent, as "room event public|private". */
  readonly receipts: string[] = [];
  /** Every attachment the window asked main to save, as "room event name". */
  readonly saved: string[] = [];
  /** Answers held back until release, by method name. */
  readonly holding = new Set<string>();
  /** The stored drafts, by room: kithd keeps them for every profile alike. */
  readonly drafts = new Map<string, StoredDraft>();
  /**
   * Each profile's rail, as Rail serves it; "gone" refuses it (the profile is no longer
   * in the config). Unset, one row holds every room, as a rail with no spaces or tags.
   */
  readonly rails = new Map<string, RailResponse | "gone">();
  /** What the next draft writes meet: a failure before the write, or after it (its answer lost). */
  readonly draftFaults: ("before" | "after")[] = [];
  /** Runs as the next draft write reaches kithd, before it is written: another writer first. */
  beforeDraft: (() => void) | undefined;
  /** Every message sent, as kithd took it; a send again under a txn ID it has is the same one. */
  readonly sent: { room: string; draft: Draft }[] = [];
  /** Sends still to answer with an error after they went out (the answer lost). */
  loseSends = 0;
  /** Every other write the window made, as "Method room target key". */
  readonly writes: string[] = [];
  /** The mention candidates, by room, and the emoji ranking, by kind; and who is me. */
  readonly members = new Map<string, Member[]>();
  readonly scores = new Map<string, Record<string, number>>();
  selves: string[] = ["@me:x"];
  /** Files offered to main and sent through it, as "room name caption". */
  readonly attached: string[] = [];
  /** Sends of files still to fail. */
  failAttach = 0;
  private held: (() => void)[] = [];
  private up = true;
  /** main's profile, as the gate keeps it. */
  private profile = "";

  of(profile: string): Profile {
    let p = this.data.get(profile);
    if (p === undefined) {
      p = { rooms: [], unread: new Map(), invites: [], messages: new Map(), reactions: [] };
      this.data.set(profile, p);
    }
    return p;
  }

  /** open is the window's streams of method still open. */
  open(method?: string): Sub[] {
    return this.subs.filter((s) => !s.closed && (method === undefined || s.method === method));
  }

  /** bridge is window.kith for this kithd. */
  bridge(): KithBridge {
    return {
      call: (name, bytes) => this.call(name, bytes),
      subscribe: (name, bytes, listener) => this.subscribe(name, bytes, listener),
      unread: (count) => {
        this.told.push(count);
      },
      save: (room, event, name) => {
        this.saved.push(`${room} ${event} ${name}`);
        return Promise.resolve(true);
      },
      pick: () => Promise.resolve([this.offer("report.pdf")]),
      offer: (file) => Promise.resolve(this.offer(file.name)),
      closing: () => undefined,
      discard: (token) => {
        this.offers.delete(token);
        return Promise.resolve();
      },
      attach: async (token, room, caption) => {
        await later();
        const name = this.offers.get(token);
        this.offers.delete(token);
        if (name === undefined || this.failAttach-- > 0) {
          throw new Error("the upload failed");
        }
        this.attached.push(`${room} ${name} ${caption}`);
        return { kept: false };
      },
    };
  }

  private readonly offers = new Map<string, string>();

  private offer(name: string): Offered {
    const token = `t${String(this.offers.size + this.attached.length + 1)}`;
    this.offers.set(token, name);
    return { token, name, size: 1 };
  }

  /** append is kith-mcp appending words to room's draft over what it read, as send.go does. */
  append(room: string, words: string): boolean {
    const held = this.drafts.get(room);
    if (held !== undefined && (held.editing !== "" || held.editSaved !== "")) {
      return false; // refused mid-edit
    }
    this.drafts.set(
      room,
      create(StoredDraftSchema, {
        roomId: room,
        body: held?.body ? `${held.body}\n\n${words}` : words,
        replyTo: held?.replyTo ?? "",
        threadRoot: held?.threadRoot ?? "",
        author: "agent",
        updatedUnixMs: (held?.updatedUnixMs ?? 0n) + 1n,
      }),
    );
    return true;
  }

  /** write is the cache writing m in profile: kept, and sent as a Messages frame once up. */
  write(profile: string, m: Message): void {
    const p = this.of(profile);
    const room = (p.messages.get(m.roomId) ?? []).filter((x) => x.id !== m.id);
    p.messages.set(m.roomId, [...room, m].sort(compare));
    if (this.up) {
      this.send(backend.messages, profile, { message: m, epoch: "e", change: 1n });
    }
  }

  /** readAgain is main telling the window kithd could not resume its Messages stream. */
  readAgain(profile: string): void {
    for (const s of this.open(backend.messages.name)) {
      if (s.profile === profile) {
        this.post(s, { kind: "reload" });
      }
    }
  }

  /** send sends a frame on every open stream of method in profile. */
  send<M extends DescMethod>(method: M, profile: string, frame: MessageInitShape<M["output"]>): void {
    for (const s of this.open(method.name)) {
      if (s.profile === profile) {
        this.post(s, { kind: "frames", frames: [toBinary(method.output, create(method.output, frame))] });
      }
    }
  }

  /**
   * setUnread changes a room's count in profile and sends it, as kithd does once up; a
   * room read to the end leaves CachedUnread's answer.
   */
  setUnread(profile: string, u: Unread): void {
    if (u.messages > 0n) {
      this.of(profile).unread.set(u.roomId, u);
    } else {
      this.of(profile).unread.delete(u.roomId);
    }
    if (this.up) {
      this.send(backend.unreadStream, profile, { unread: u });
    }
  }

  /** setInvites changes profile's invitations and sends the whole set. */
  setInvites(profile: string, invites: Room[]): void {
    this.of(profile).invites = invites;
    if (this.up) {
      this.send(backend.invites, profile, { invites });
    }
  }

  /** otherTakesOver is another window taking profile's seat by force: whoever sits steps aside. */
  otherTakesOver(profile: string): void {
    const holder = this.seats.get(profile);
    if (holder !== undefined && holder !== "other") {
      // As kithd: the window is told to step aside, and keeps the seat (its last draft
      // writes taken) until it closes the seat's stream.
      this.taking.add(profile);
      this.send1(holder, backend.seat, { event: { case: "stepAside", value: true } });
      return;
    }
    this.seats.set(profile, "other");
  }

  /** Profiles whose seat goes to another window once the one holding it lets go. */
  private readonly taking = new Set<string>();

  /** down is kithd stopping: every stream ends, and the seats are forgotten. */
  down(): void {
    this.up = false;
    this.seats.clear();
    for (const s of this.open()) {
      this.post(s, { kind: "detached", error: "kithd went away" });
    }
  }

  /** again is kithd back: main opens every stream again, a forced seat unforced. */
  again(): void {
    this.up = true;
    for (const s of this.open()) {
      if (s.method === backend.seat.name) {
        (s.request as SeatRequest).force = false;
      }
      this.attach(s);
    }
  }

  /** heldCalls is how many answers are held back now. */
  get heldCalls(): number {
    return this.held.length;
  }

  /** releaseAt answers one call held back, the i-th of those held (wrapping). */
  releaseAt(i: number): void {
    if (this.held.length > 0) {
      this.held.splice(i % this.held.length, 1)[0]?.();
    }
  }

  /** release answers every call held back, in the order they came or the other way round. */
  release(reversed = false): void {
    const held = reversed ? this.held.reverse() : this.held;
    this.held = [];
    held.forEach((answer) => {
      answer();
    });
  }

  private async call(name: string, bytes: Uint8Array): Promise<Uint8Array> {
    const method = methods.get(name);
    if (method?.methodKind !== "unary") {
      throw new Error(`no call ${name}`);
    }
    const device = method.parent.typeName === DeviceService.typeName;
    const profile = this.profile;
    if (!device && profile === "") {
      this.refused.push(name);
      throw new Error(`refused: ${name} with no profile`);
    }
    this.calls.push(`${name}:${device ? "" : profile}`);
    const request = fromBinary(method.input, bytes);
    await later();
    if (!this.up && !device) {
      throw new Error("kithd is not reachable");
    }
    // Read as kithd reads it, now; held back, the answer is on its way meanwhile.
    const answer = this.answer(name, profile, request);
    if (this.holding.has(name)) {
      await new Promise<void>((resolve) => this.held.push(resolve));
    }
    if (name === DeviceService.method.device.name) {
      const d = answer as MessageShape<typeof DeviceService.method.device.output>;
      this.use(runIn(this.profile, d.profile, d.profiles));
    } else if (name === DeviceService.method.chooseProfile.name) {
      this.use((answer as MessageShape<typeof DeviceService.method.chooseProfile.output>).profile);
    }
    return toBinary(method.output, answer);
  }

  /** use is main moving to profile: every stream opened in another ends. */
  private use(profile: string): void {
    if (profile === this.profile) {
      return;
    }
    this.profile = profile;
    for (const s of this.open()) {
      if (s.profile !== profile) {
        // Main posts the refusal, then closes the port: it is the stream's last word.
        this.close(s);
        void later().then(() => {
          s.listener({ kind: "refused", error: `left ${s.profile}` });
        });
      }
    }
  }

  private close(sub: Sub): void {
    sub.closed = true;
    if (this.seats.get(sub.profile) === sub) {
      // Closing the seat's stream frees it, or hands it to the window taking it over.
      if (this.taking.delete(sub.profile)) {
        this.seats.set(sub.profile, "other");
      } else {
        this.seats.delete(sub.profile);
      }
    }
  }

  private answer(name: string, profile: string, request: MessageShape<DescMethod["input"]>) {
    const p = this.of(profile);
    const reply = <M extends DescMethod>(m: M, init: MessageInitShape<M["output"]>) => create(m.output, init);
    switch (name) {
      case "Device":
        return reply(DeviceService.method.device, {
          socket: "/run/kithd.sock",
          profile: this.profiles.includes(this.remembered) ? this.remembered : "",
          profiles: this.profiles,
        });
      case "ChooseProfile": {
        const want = (request as MessageShape<typeof DeviceService.method.chooseProfile.input>).name;
        const chosen = this.profiles.find((n) => n.toLowerCase() === want.toLowerCase());
        if (chosen === undefined) {
          throw new Error(`no profile ${want}`);
        }
        this.remembered = chosen;
        return reply(DeviceService.method.chooseProfile, { profile: chosen });
      }
      case "Status":
        return reply(backend.status, { ready: true });
      case "Rooms":
        return reply(backend.rooms, { rooms: p.rooms });
      case "Rail": {
        const rail = this.rails.get(profile);
        if (rail === "gone") {
          throw new Error(`daemon: rail: unknown profile ${profile}`);
        }
        return (
          rail ??
          create(RailResponseSchema, {
            revision: 1n,
            rows: [{ key: "all", label: "All", kind: RailRowKind.ALL, rooms: p.rooms.map((r) => r.id) }],
            rooms: p.rooms.map((r) => ({ facts: { id: r.id, name: r.name } })),
          })
        );
      }
      case "LastMessages":
        return reply(backend.lastMessages, {});
      case "CachedUnread":
        return reply(backend.cachedUnread, { unread: [...p.unread.values()] });
      case "CachedInvites":
        return reply(backend.cachedInvites, { invites: p.invites });
      case "JoinRoom":
      case "LeaveRoom":
        return reply(backend.leaveRoom, {});
      case "DeviceSettings":
        if (this.settings === "fail") {
          throw new Error("reading device.toml: it does not load");
        }
        return this.settings;
      case "CachedTimeline": {
        const room = (request as MessageShape<typeof backend.cachedTimeline.input>).roomId;
        return reply(backend.cachedTimeline, { messages: (p.messages.get(room) ?? []).slice(-4) });
      }
      case "Timeline":
        throw new Error("the network is off"); // its pages hand over to the cache's
      case "MessagesAround": {
        const r = request as MessageShape<typeof backend.messagesAround.input>;
        const all = p.messages.get(r.roomId) ?? [];
        const at = all.findIndex((m) => m.id === r.eventId);
        return reply(backend.messagesAround, { messages: all.slice(Math.max(0, at - Number(r.before)), at + 1) });
      }
      case "FetchEvent": {
        const r = request as MessageShape<typeof backend.fetchEvent.input>;
        const found = (p.messages.get(r.roomId) ?? []).find((m) => m.id === r.eventId);
        if (found === undefined) {
          throw new Error(`no event ${r.eventId}`);
        }
        return reply(backend.fetchEvent, { message: found });
      }
      case "CachedReactions": {
        const room = (request as MessageShape<typeof backend.cachedReactions.input>).roomId;
        return reply(backend.cachedReactions, { reactions: p.reactions.filter((r) => r.roomId === room) });
      }
      case "Drafts":
        return reply(backend.drafts, { drafts: [...this.drafts.values()] });
      case "ReplaceDraft": {
        const r = request as MessageShape<typeof backend.replaceDraft.input>;
        if (this.seats.get(profile) === "other") {
          throw new Error("daemon: kith is open in another window"); // seats.write
        }
        const hook = this.beforeDraft;
        this.beforeDraft = undefined;
        hook?.();
        if (this.draftFaults[0] === "before") {
          this.draftFaults.shift();
          throw new Error("unavailable: connection reset");
        }
        const draft = r.draft;
        if (draft === undefined || !r.rebase) {
          throw new Error("the fake takes a window's rebased writes only");
        }
        const [stored, added] = rebased(draft, r.over, this.drafts.get(draft.roomId), r.unanswered);
        if (blank(stored)) {
          this.drafts.delete(draft.roomId);
        } else {
          this.drafts.set(draft.roomId, stored);
        }
        if (this.draftFaults[0] === "after") {
          this.draftFaults.shift();
          throw new Error("unavailable: connection reset");
        }
        return reply(backend.replaceDraft, { saved: true, stored: blank(stored) ? undefined : stored, added });
      }
      case "Send": {
        const r = request as MessageShape<typeof backend.send.input>;
        if (r.draft !== undefined && !this.sent.some((s) => s.draft.txnId !== "" && s.draft.txnId === r.draft?.txnId)) {
          this.sent.push({ room: r.roomId, draft: r.draft });
        }
        if (this.loseSends > 0) {
          this.loseSends--;
          throw new Error("unavailable: connection reset");
        }
        return reply(backend.send, {});
      }
      case "Redact": {
        const r = request as MessageShape<typeof backend.redact.input>;
        this.writes.push(`Redact ${r.roomId} ${r.eventId}`);
        return reply(backend.redact, {});
      }
      case "SendReaction": {
        const r = request as MessageShape<typeof backend.sendReaction.input>;
        this.writes.push(`SendReaction ${r.roomId} ${r.target} ${r.key}`);
        return reply(backend.sendReaction, {});
      }
      case "SendTyping": {
        const r = request as MessageShape<typeof backend.sendTyping.input>;
        this.writes.push(`SendTyping ${r.roomId} ${String(r.typing)}`);
        return reply(backend.sendTyping, {});
      }
      case "RecordEmoji": {
        const r = request as MessageShape<typeof backend.recordEmoji.input>;
        this.writes.push(`RecordEmoji ${r.roomId} ${r.kind} ${r.emoji}`);
        return reply(backend.recordEmoji, {});
      }
      case "MentionCandidates": {
        const r = request as MessageShape<typeof backend.mentionCandidates.input>;
        return reply(backend.mentionCandidates, { members: this.members.get(r.roomId) ?? [] });
      }
      case "EmojiScores": {
        const r = request as MessageShape<typeof backend.emojiScores.input>;
        return reply(backend.emojiScores, { scores: this.scores.get(r.kind) ?? {} });
      }
      case "Selves":
        return reply(backend.selves, { ids: this.selves });
      case "MarkRead": {
        const r = request as MessageShape<typeof backend.markRead.input>;
        this.receipts.push(`${r.roomId} ${r.eventId} ${r.private ? "private" : "public"}`);
        return reply(backend.markRead, {});
      }
      default:
        throw new Error(`the fake does not answer ${name}`);
    }
  }

  private subscribe(name: string, bytes: Uint8Array, listener: (event: StreamEvent) => void): () => void {
    const method = methods.get(name);
    if (method?.methodKind !== "server_streaming") {
      throw new Error(`no stream ${name}`);
    }
    const sub: Sub = {
      method: name,
      profile: this.profile,
      request: fromBinary(method.input, bytes),
      listener,
      closed: false,
    };
    if (this.profile === "") {
      this.refused.push(name);
      this.post(sub, { kind: "refused", error: `refused: ${name} with no profile` });
      return () => undefined;
    }
    this.subs.push(sub);
    if (this.up) {
      this.attach(sub);
    }
    return () => {
      this.close(sub);
    };
  }

  /** attach opens sub at kithd: attached, then what a Seat answers. */
  private attach(sub: Sub): void {
    this.post(sub, { kind: "attached" });
    if (sub.method === backend.messages.name) {
      // The first frame says where the stream starts (main resumes it from the last).
      this.send1(sub, backend.messages, { epoch: "e", change: 0n });
    }
    if (sub.method !== backend.seat.name) {
      return;
    }
    const ask = sub.request as SeatRequest;
    const holder = this.seats.get(sub.profile);
    if (holder === undefined || ask.force) {
      if (holder !== undefined && holder !== "other") {
        this.send1(holder, backend.seat, { event: { case: "stepAside", value: true } });
      }
      this.seats.set(sub.profile, sub);
      this.send1(sub, backend.seat, { event: { case: "granted", value: true } });
    } else {
      this.send1(sub, backend.seat, { event: { case: "taken", value: { pid: 4312n, host: "elsewhere" } } });
      this.post(sub, { kind: "detached", error: "the stream ended" });
    }
  }

  private send1<M extends DescMethod>(sub: Sub, method: M, frame: MessageInitShape<M["output"]>): void {
    this.post(sub, { kind: "frames", frames: [toBinary(method.output, create(method.output, frame))] });
  }

  /** post delivers an event later, as a port does, and never once the stream is closed. */
  private post(sub: Sub, event: StreamEvent): void {
    void later().then(() => {
      if (!sub.closed) {
        sub.listener(event);
      }
    });
  }
}

/** blank: a stored draft with nothing worth keeping. */
function blank(d: StoredDraft): boolean {
  return d.body === "" && d.replyTo === "" && d.editing === "" && d.editSaved === "";
}

/**
 * rebased is kithd's rebase of a window's write (domain.RebaseDraft, in Go): what to
 * store when the window wrote draft over base and current is stored, and the words
 * another writer added since. The Go rule's own tests are its tables; this is the fake's.
 */
function rebased(
  draft: StoredDraft,
  base: StoredDraft | undefined,
  current: StoredDraft | undefined,
  own: readonly StoredDraft[],
): [StoredDraft, string] {
  const none = create(StoredDraftSchema);
  const now = current ?? none;
  const words = (d: StoredDraft) => (d.editing !== "" ? d.editSaved : d.body);
  const targets = (a: StoredDraft, b: StoredDraft) =>
    a.replyTo === b.replyTo &&
    a.threadRoot === b.threadRoot &&
    a.editing === b.editing &&
    (a.editing === "" || a.body === b.body);
  const same = (a: StoredDraft, b: StoredDraft) =>
    targets(a, b) && a.body === b.body && a.editSaved === b.editSaved && a.updatedUnixMs === b.updatedUnixMs;
  const extends_ = (text: string, from: string) =>
    text.startsWith(from) &&
    (text.length === from.length || from === "" || /^[ \n]/.test(text.slice(from.length)) || /[ \n]$/.test(from));
  let over = own.find((o) => same(o, now));
  if (over === undefined) {
    const under = (v: StoredDraft) => targets(v, now) && extends_(words(now), words(v));
    over = base ?? none;
    let longest = under(over) ? words(over).length : -1;
    for (const o of own) {
      if (words(o) !== "" && under(o) && words(o).length >= longest) {
        over = o;
        longest = words(o).length;
      }
    }
  }
  const mine = words(draft);
  const stored = words(now);
  const theirs =
    stored === mine
      ? ""
      : words(over) !== "" && extends_(stored, words(over))
        ? stored.slice(words(over).length).replace(/^[ \n]+/, "")
        : stored;
  const all = theirs === "" ? mine : mine === "" ? theirs : `${mine}\n\n${theirs}`;
  return [draft.editing !== "" ? { ...draft, editSaved: all } : { ...draft, body: all }, theirs];
}

/** later is a turn of the event loop: answers and events arrive after the call returns. */
function later(): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

/** settled waits until everything due has arrived. */
export async function settled(): Promise<void> {
  for (let i = 0; i < 20; i++) {
    await later();
  }
}
