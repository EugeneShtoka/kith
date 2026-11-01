// A fake of what the window reaches through window.kith, for the window's tests: kithd
// (its seats, rooms, unread and invitations, per profile), the relay (profiles and the
// one remembered) and main between them (the profile every request carries, decided as
// the gate decides it; streams opened again after kithd restarts, a forced seat asked
// for again unforced). Answers and events arrive later, as over IPC.
import {
  clone,
  create,
  type DescMethod,
  fromBinary,
  type MessageInitShape,
  type MessageShape,
  toBinary,
} from "@bufbuild/protobuf";

import {
  BackendService,
  type ConfigSetting,
  ConfigSettingSchema,
  type ConfigValue,
  type FollowStreamResponse,
  type Misspelling,
  MisspellingSchema,
  type RailResponse,
  RailResponseSchema,
  RailRowKind,
  type ScheduledMessage,
  ScheduledMessageSchema,
  type SeatRequest,
  type StoredDraft,
  StoredDraftSchema,
} from "../../gen/backend/v1/services_pb";
import {
  type Draft,
  DraftSchema,
  type Member,
  type Message,
  type Reaction,
  type Room,
  RoomSchema,
  type Unread,
  UnreadSchema,
} from "../../gen/backend/v1/types_pb";
import {
  type DeviceSettings,
  DeviceSettingsSchema,
  DeviceService,
  type ThemeResponse,
  ThemeResponseSchema,
  ThemeScope,
  ThemeSource,
  ThemeValueSchema,
} from "../../gen/device/v1/device_pb";
import { same, words } from "../config";
import { compare } from "../merge";
import type { KithBridge, ThemeMode, Offered, StreamEvent } from "../../shared/ipc";
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
  /** Ended: by main (what it posted before still arrives), or by the window (dropped). */
  closed: boolean;
  dropped: boolean;
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
  /** The window's text size, as main keeps it. */
  fontSize = 15;
  /** The theme's mode the window last told main. */
  themeMode: ThemeMode | undefined;
  /**
   * The relay's theme: config.toml's [gui.theme] (through kithd) and device.toml's, by key,
   * and config.toml's revision (device.toml's is settingsRevision); the defaults' sources.
   */
  readonly themeShared = new Map<string, string>();
  readonly themeDevice = new Map<string, string>();
  configRevision = 1;
  /** A [gui.theme] edited by hand into one that does not check: the relay refuses to read it. */
  themeFails = false;
  themeDefaults: ThemeResponse = create(ThemeResponseSchema);
  /** Each room's remembered sender colors (SenderSlots). */
  readonly slots = new Map<string, Map<string, number>>();
  /** What the window told main to count in the tray, in order. */
  readonly told: number[] = [];
  /** The bridge the device joined, "" for a kithd of its own: the relay reads the profiles through it. */
  joined = "";
  /** The relay's device settings, or "fail" for a device file that does not load. */
  settings: DeviceSettings | "fail" = create(DeviceSettingsSchema, {
    sendReceipts: true,
    unreadLine: true,
  });
  /**
   * The bridge's settings as kithd lists them (ConfigSettings), config.toml's revision
   * being configRevision (the theme's file too); every setting set, as
   * "path=words:profile". A set over a revision gone by is ABORTED, and configRefusal
   * refuses one as a config kithd would not run with.
   */
  configSettings: ConfigSetting[] = [];
  readonly configSets: string[] = [];
  configRefusal = "";
  /** Runs as the next SetConfigSetting reaches kithd: another writer of config.toml first. */
  beforeConfigWrite: (() => void) | undefined;
  /** device.toml's revision: each write makes a new one. */
  settingsRevision = 1;
  /** Runs as the next settings write reaches the relay: another writer of device.toml first. */
  beforeSettingsWrite: (() => void) | undefined;
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
  readonly sent: { room: string; draft: Draft; queued?: ScheduledMessage }[] = [];
  /** Sends still to answer with an error after they went out (the answer lost). */
  loseSends = 0;
  /**
   * The send queue as kithd's scheduler keeps it: one entry per transaction ID (a Schedule
   * asked again under one is answered with the entry it has, or the one it sent). What the
   * next Schedules meet: a failure before kithd queues it, or after (the answer lost).
   */
  readonly queue: ScheduledMessage[] = [];
  readonly scheduleFaults: ("before" | "after")[] = [];
  private queued = 0;
  /** The commands folder's commands, what each run answers, and each run, as "name room event link:profile". */
  userCommands: { name: string; applies: boolean; places?: string[] }[] = [];
  readonly ranWith = new Map<string, MessageInitShape<typeof DeviceService.method.runCommand.output>>();
  readonly runs: string[] = [];
  /** Every other write the window made, as "Method room target key". */
  readonly writes: string[] = [];
  /** Who chose what in each poll, by its message's ID (PollVoters); a message not here asks none. */
  readonly pollVoters = new Map<string, MessageInitShape<typeof backend.pollVoters.output>>();
  /** Rooms whose network asks no polls: CreatePoll is refused there, as kithd's router refuses it. */
  readonly noPolls = new Set<string>();
  /** The mention candidates, by room, and the emoji ranking, by kind; and who is me. */
  readonly members = new Map<string, Member[]>();
  readonly scores = new Map<string, Record<string, number>>();
  selves: string[] = ["@me:x"];
  /** The accounts kithd has (Accounts), and what each verification answer met: kithd's error, or none. */
  accounts: MessageInitShape<typeof backend.accounts.output>["accounts"] = [
    { network: "Matrix", on: "matrix:1/", name: "@me:x" },
  ];
  readonly verifyFails = new Map<string, string>();
  /** Files offered to main and sent through it, as "room name caption". */
  readonly attached: string[] = [];
  /** Sends of files still to fail. */
  failAttach = 0;
  /** Cached pages (CachedTimeline, MessagesAround) still to fail, and pages of a thread (ThreadPage). */
  failPages = 0;
  failThreadPages = 0;
  /** The links that reached the window, in order (main posted them, the window's port took them). */
  readonly linksArrived: FollowStreamResponse[] = [];
  /** What each window last told kithd it has open (Present), by client. */
  readonly presented = new Map<string, string>();
  /** The networks to log in to; the steps a login goes through, in order; the logins canceled. */
  loginNetworks: MessageInitShape<typeof backend.loginNetworks.output> = {};
  readonly loginSteps: MessageInitShape<typeof backend.beginLogin.output>["step"][] = [];
  readonly loginsCanceled: string[] = [];
  /** The accounts written into the config (WriteAccount), and the spaces signed out of. */
  readonly accountsWritten: string[] = [];
  readonly signedOut: string[] = [];
  /**
   * Spelling, as kithd's engine answers it: the words it rejects, with what it offers
   * instead (none listed: it accepts the word); the words it was taught, for good or for
   * the session; and spellOff, nothing to check with (FAILED_PRECONDITION).
   */
  readonly misspelled = new Map<string, string[]>();
  readonly learned: string[] = [];
  spellOff = false;
  /**
   * Filing: the places kithd offers each room (FilingPlaces, for a room of the profile
   * only, as kithd checks it), and a [display] filing_spaces naming none; every filing
   * asked, as "room tag+|tag-|space+|space-…:profile", and what FileRoom answers or the
   * error it refuses with.
   */
  readonly places = new Map<string, MessageInitShape<typeof backend.filingPlaces.output>["places"]>();
  /**
   * The phonebook, as kithd serves it from its folder: each card by name, the people it
   * is of (the first by name holding a person is theirs), its identifiers, fields and
   * revision, and the room that is your conversation with them; every field written,
   * as "card key=values:profile". A write over a revision gone by is ABORTED. People
   * narrows by words in the name and key:value terms, as kithd does.
   */
  readonly cards = new Map<
    string,
    {
      people: string[];
      ids?: Record<string, string[]>;
      fields: Record<string, string[]>;
      revision: number;
      conversation: string;
    }
  >();
  readonly cardWrites: string[] = [];
  readonly filingSpacesAsked: string[][] = [];
  readonly filed: string[] = [];
  fileAnswer: MessageInitShape<typeof backend.fileRoom.output> = {};
  fileRefusal = "";
  /** The words CompleteWord knows, with their weight; and the model's answer to ModelTask. */
  readonly vocabulary = new Map<string, number>();
  model: MessageInitShape<typeof backend.modelTask.output> = { refusal: "no model is installed" };
  /** What DetectLanguages and DetectModel offer, and what was installed, as "Method tag". */
  languages: MessageInitShape<typeof backend.detectLanguages.output> = {};
  modelOffer: MessageInitShape<typeof backend.detectModel.output> = {};
  readonly installed: string[] = [];
  /**
   * Pairing, as kithd and main answer it: kithd's own ID and the devices paired; the QR
   * code's modules the relay draws; what a pairing's stream sends as it opens (the ticket,
   * then the device that answered, if any); what main's dialog answers (pairSays: the
   * person's yes); and every answer and unpairing that reached kithd, as "Method arg".
   */
  self = "";
  paired: MessageInitShape<typeof backend.devices.output>["devices"] = [];
  qr = new Uint8Array([1, 0, 0, 1]);
  pairing: MessageInitShape<typeof backend.startPairing.output>[] = [];
  pairSays = true;
  readonly pairingCalls: string[] = [];
  /** What a join's stream sends as it opens: the digits, then the bridge joined, if any. */
  joining: MessageInitShape<typeof DeviceService.method.joinBridge.output>[] = [];
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
      fontSize: () => Promise.resolve(this.fontSize),
      setFontSize: (px) => {
        this.fontSize = px;
        return Promise.resolve(px);
      },
      themeMode: (mode) => {
        this.themeMode = mode;
      },
    };
  }

  private readonly offers = new Map<string, string>();

  private offer(name: string): Offered {
    const token = `t${String(this.offers.size + this.attached.length + 1)}`;
    this.offers.set(token, name);
    return { token, name, size: 1 };
  }

  /**
   * fire is kithd's scheduler sending the queued message id, its time come: out of the
   * queue, and sent under its transaction ID (a send again under it is the same message).
   */
  fire(id: string): void {
    const at = this.queue.findIndex((q) => q.id === id);
    const q = this.queue[at];
    if (q === undefined) {
      return;
    }
    this.queue.splice(at, 1);
    if (!this.sent.some((s) => s.draft.txnId === q.txnId)) {
      const draft = create(DraftSchema, { body: q.body, replyTo: q.replyTo, threadRoot: q.threadRoot, txnId: q.txnId });
      this.sent.push({ room: q.roomId, draft, queued: q });
    }
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

  /**
   * setsConfig is another writer of config.toml (the TUI's settings screen, a hand edit,
   * another window) setting path: a new revision, told on every ConfigChanged stream.
   */
  setsConfig(path: string, value: ConfigValue | undefined): void {
    const s = this.configSettings.find((x) => x.path === path);
    if (s !== undefined) {
      s.value = value;
    }
    this.configChanged();
  }

  /** configChanged is config.toml at a new revision, told on every ConfigChanged stream, the config's text left out. */
  configChanged(): void {
    this.configRevision++;
    for (const s of this.open(backend.configChanged.name)) {
      this.post(s, {
        kind: "frames",
        frames: [
          toBinary(
            backend.configChanged.output,
            create(backend.configChanged.output, { revision: String(this.configRevision) }),
          ),
        ],
      });
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
    // The relay's calls in a profile carry it as kithd's do (the gate's profiledDevice).
    const device =
      method.parent.typeName === DeviceService.typeName &&
      name !== DeviceService.method.commands.name &&
      name !== DeviceService.method.runCommand.name;
    const profile = this.profile;
    if (!device && profile === "") {
      this.refused.push(name);
      throw new Error(`refused: ${name} with no profile`);
    }
    this.calls.push(`${name}:${device ? "" : profile}`);
    const request = fromBinary(method.input, bytes);
    await later();
    if (!this.up && (!device || this.joined !== "")) {
      throw new Error("kithd is not reachable");
    }
    // Read as kithd reads it, now; held back, the answer (or the refusal) is on its way meanwhile.
    const answered = (() => {
      try {
        return { answer: this.answer(name, profile, request) };
      } catch (err) {
        return { err };
      }
    })();
    if (this.holding.has(name)) {
      await new Promise<void>((resolve) => this.held.push(resolve));
    }
    if ("err" in answered) {
      throw answered.err;
    }
    const answer = answered.answer;
    if (name === DeviceService.method.device.name) {
      const d = answer as MessageShape<typeof DeviceService.method.device.output>;
      this.use(runIn(this.profile, d.profile, d.profiles));
    } else if (name === DeviceService.method.chooseProfile.name) {
      this.use((answer as MessageShape<typeof DeviceService.method.chooseProfile.output>).profile);
    }
    return toBinary(method.output, answer);
  }

  /** cardOf is the card named, or the person's: the first by name holding them ("" none). */
  private cardOf(person: string, card: string): string {
    if (card !== "") {
      if (!this.cards.has(card)) {
        throw new Error(`[not_found] daemon: the phonebook has no card "${card}"`);
      }
      return card;
    }
    return [...this.cards.keys()].sort().find((n) => this.cards.get(n)?.people.includes(person)) ?? "";
  }

  /** shownCard is the card named as kithd answers it; "" a person with none. */
  private shownCard(name: string): MessageInitShape<typeof backend.personCard.output>["card"] {
    const c = this.cards.get(name);
    return {
      card: c === undefined ? "" : name,
      revision: c === undefined ? "" : String(c.revision),
      ids: Object.entries(c?.ids ?? {}).map(([key, values]) => ({ key, values, list: true })),
      fields: Object.entries(c?.fields ?? {}).map(([key, values]) => ({ key, values, list: values.length > 1 })),
    };
  }

  /** use is main moving to profile: every stream opened in another ends. */
  private use(profile: string): void {
    if (profile === this.profile) {
      return;
    }
    this.profile = profile;
    for (const s of this.open()) {
      if (s.profile !== profile) {
        // Main posts the refusal, then closes the port: it is the stream's last word, after
        // whatever it posted before.
        this.close(s);
        void later().then(() => {
          if (!s.dropped) {
            s.listener({ kind: "refused", error: `left ${s.profile}` });
          }
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
    if ((name === "CachedTimeline" || name === "MessagesAround") && this.failPages-- > 0) {
      throw new Error("cache: database is locked");
    }
    if (name === "ThreadPage" && this.failThreadPages-- > 0) {
      throw new Error("matrix: thread page: connection refused");
    }
    switch (name) {
      case "Device":
        return reply(DeviceService.method.device, {
          socket: this.joined === "" ? "/run/kithd.sock" : "",
          bridge: this.joined,
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
        return reply(DeviceService.method.deviceSettings, {
          settings: this.settings,
          revision: String(this.settingsRevision),
        });
      case "UpdateDeviceSettings": {
        // As the relay: written only over the file at the revision read.
        const r = request as MessageShape<typeof DeviceService.method.updateDeviceSettings.input>;
        const before = this.beforeSettingsWrite;
        this.beforeSettingsWrite = undefined;
        before?.();
        if (this.settings === "fail") {
          throw new Error("reading device.toml: it does not load");
        }
        if (r.baseRevision !== String(this.settingsRevision)) {
          throw new Error("ConnectError: [aborted] device.toml changed since it was read");
        }
        this.settings = r.settings ?? this.settings;
        this.settingsRevision++;
        return reply(DeviceService.method.updateDeviceSettings, {
          settings: this.settings,
          revision: String(this.settingsRevision),
        });
      }
      case "Theme":
        if (this.themeFails) {
          throw new Error('ConnectError: [failed_precondition] config.toml: theme.dark.accent: "blue" is no color');
        }
        return reply(DeviceService.method.theme, this.theme());
      case "UpdateTheme": {
        // As the relay: each file written only at the revision read.
        const r = request as MessageShape<typeof DeviceService.method.updateTheme.input>;
        for (const w of r.writes) {
          const device = w.scope === ThemeScope.DEVICE;
          const at = device ? this.settingsRevision : this.configRevision;
          if ((device ? r.deviceRevision : r.configRevision) !== String(at)) {
            throw new Error("ConnectError: [aborted] the file changed since it was read");
          }
          const file = device ? this.themeDevice : this.themeShared;
          if (w.value === "") {
            file.delete(w.key);
          } else {
            file.set(w.key, w.value);
          }
          if (device) {
            this.settingsRevision++;
          } else {
            this.configRevision++;
          }
        }
        return reply(DeviceService.method.updateTheme, { theme: this.theme() });
      }
      case "SenderSlots": {
        const room = (request as MessageShape<typeof backend.senderSlots.input>).roomId;
        return reply(backend.senderSlots, {
          slots: [...(this.slots.get(room) ?? new Map<string, number>())].map(([key, slot]) => ({ key, slot })),
        });
      }
      case "CachedTimeline": {
        const room = (request as MessageShape<typeof backend.cachedTimeline.input>).roomId;
        return reply(backend.cachedTimeline, { messages: (p.messages.get(room) ?? []).slice(-4) });
      }
      case "SearchMessages": {
        // As kithd: in the profile's rooms only, those asked for; newest first.
        const r = request as MessageShape<typeof backend.searchMessages.input>;
        const asked = (room: string) => r.allRooms || r.roomIds.includes(room);
        const hits = p.rooms
          .filter((room) => asked(room.id))
          .flatMap((room) => p.messages.get(room.id) ?? [])
          .filter((m) =>
            r.query !== ""
              ? m.body.toLowerCase().includes(r.query.toLowerCase())
              : r.senders.length > 0 && r.senders.includes(m.sender),
          )
          .sort(compare)
          .reverse()
          .slice(0, Number(r.limit))
          .map((m) => ({ roomId: m.roomId, eventId: m.id, sender: m.sender, timestamp: m.timestamp, snippet: m.body }));
        return reply(backend.searchMessages, { hits });
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
      case "Schedule": {
        const m = (request as MessageShape<typeof backend.schedule.input>).message;
        if (m === undefined) {
          throw new Error("no message");
        }
        if (this.scheduleFaults[0] === "before") {
          this.scheduleFaults.shift();
          throw new Error("unavailable: connection reset");
        }
        const known =
          this.queue.find((q) => q.txnId === m.txnId) ??
          this.sent.find((s) => s.draft.txnId === m.txnId && s.queued !== undefined)?.queued;
        const id = known?.id ?? `q${String(++this.queued)}`;
        if (known === undefined) {
          this.queue.push(create(ScheduledMessageSchema, { ...m, id }));
        }
        if (this.scheduleFaults[0] === "after") {
          this.scheduleFaults.shift();
          throw new Error("unavailable: connection reset");
        }
        return reply(backend.schedule, { id });
      }
      case "ScheduledMessages":
        return reply(backend.scheduledMessages, { messages: [...this.queue] });
      case "CancelScheduled": {
        const id = (request as MessageShape<typeof backend.cancelScheduled.input>).id;
        const at = this.queue.findIndex((q) => q.id === id);
        if (at >= 0) {
          this.queue.splice(at, 1);
        }
        this.writes.push(`CancelScheduled ${id}`);
        return reply(backend.cancelScheduled, { canceled: at >= 0 });
      }
      case "RetryScheduled": {
        const id = (request as MessageShape<typeof backend.retryScheduled.input>).id;
        this.writes.push(`RetryScheduled ${id}`);
        const q = this.queue.find((x) => x.id === id);
        if (q !== undefined) {
          this.fire(q.id);
        }
        return reply(backend.retryScheduled, {});
      }
      case "Commands":
        return reply(DeviceService.method.commands, { commands: this.userCommands });
      case "RunCommand": {
        const r = request as MessageShape<typeof DeviceService.method.runCommand.input>;
        if (!this.userCommands.some((c) => c.name === r.name)) {
          throw new Error(`[not_found] /${r.name}: no command of that name`);
        }
        this.runs.push(`${r.name} ${r.roomId} ${r.eventId} ${String(r.link)}:${profile}`);
        return reply(DeviceService.method.runCommand, this.ranWith.get(r.name) ?? { output: "compose" });
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
      case "VotePoll": {
        const r = request as MessageShape<typeof backend.votePoll.input>;
        this.writes.push(`VotePoll ${r.roomId} ${r.eventId} ${r.options.join(",")}`);
        return reply(backend.votePoll, {});
      }
      case "PollVoters": {
        const r = request as MessageShape<typeof backend.pollVoters.input>;
        const voters = this.pollVoters.get(r.eventId);
        if (voters === undefined) {
          throw new Error(`matrix: ${r.eventId} asks no poll`);
        }
        return reply(backend.pollVoters, voters);
      }
      case "CreatePoll": {
        const r = request as MessageShape<typeof backend.createPoll.input>;
        if (this.noPolls.has(r.roomId)) {
          throw new Error("daemon: that room's network cannot do this: creating polls in whatsapp rooms");
        }
        this.writes.push(`CreatePoll ${r.roomId} ${r.question} ${r.options.join("|")} ${String(r.multiple)}`);
        return reply(backend.createPoll, { eventId: "$asked" });
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
      case "Accounts":
        return reply(backend.accounts, { accounts: this.accounts });
      case "StartVerification": {
        const r = request as MessageShape<typeof backend.startVerification.input>;
        this.writes.push(`StartVerification ${r.on}`);
        return reply(backend.startVerification, { txnId: "ours" });
      }
      case "AcceptVerification":
      case "ConfirmSAS":
      case "CancelVerification": {
        const r = request as MessageShape<typeof backend.confirmSAS.input>;
        this.writes.push(`${name} ${r.txnId}`);
        const fails = this.verifyFails.get(name);
        if (fails !== undefined) {
          throw new Error(fails);
        }
        return reply(backend.confirmSAS, {});
      }
      case "Selves":
        return reply(backend.selves, { ids: this.selves });
      case "MarkRead": {
        const r = request as MessageShape<typeof backend.markRead.input>;
        this.receipts.push(`${r.roomId} ${r.eventId} ${r.private ? "private" : "public"}`);
        return reply(backend.markRead, {});
      }
      case "MarkThreadRead": {
        const r = request as MessageShape<typeof backend.markThreadRead.input>;
        this.receipts.push(`${r.roomId} ${r.root} ${r.eventId} ${r.private ? "private" : "public"}`);
        return reply(backend.markThreadRead, {});
      }
      case "Present": {
        const r = request as MessageShape<typeof backend.present.input>;
        this.presented.set(r.client, r.openRoomId);
        return reply(backend.present, {});
      }
      case "ThreadPage": {
        // The thread's replies before from, newest first, two a page (Matrix's relations).
        const r = request as MessageShape<typeof backend.threadPage.input>;
        const replies = (p.messages.get(r.roomId) ?? []).filter((m) => m.threadRoot === r.root);
        const before = r.from === "" ? replies.length : replies.findIndex((m) => m.id === r.from);
        const from = Math.max(0, before - 2);
        const next = from > 0 ? (replies[from]?.id ?? "") : "";
        return reply(backend.threadPage, { page: { messages: replies.slice(from, before), next } });
      }
      case "LoginNetworks":
        return reply(backend.loginNetworks, this.loginNetworks);
      case "BeginLogin":
        return reply(backend.beginLogin, { step: this.loginSteps.shift() });
      case "AnswerLogin":
        return reply(backend.answerLogin, { step: this.loginSteps.shift() });
      case "CancelLogin":
        this.loginsCanceled.push((request as MessageShape<typeof backend.cancelLogin.input>).login);
        return reply(backend.cancelLogin, {});
      case "WriteAccount": {
        const r = request as MessageShape<typeof DeviceService.method.writeAccount.input>;
        this.accountsWritten.push(`${r.table} ${r.values.name ?? ""}`);
        return reply(DeviceService.method.writeAccount, {});
      }
      case "QRCode":
        return reply(DeviceService.method.qRCode, { dark: this.qr });
      case "Devices":
        return reply(backend.devices, { self: this.self, devices: this.paired });
      case "PairingURI": {
        const t = request as MessageShape<typeof DeviceService.method.pairingURI.input>;
        const q = new URLSearchParams([...t.addresses.map((a) => ["addr", a]), ["id", t.id], ["token", t.token]]);
        return reply(DeviceService.method.pairingURI, { uri: `kith://pair?${q.toString()}` });
      }
      case "ConfirmPairing": {
        // Main's dialog asks the person; their no refuses it.
        const r = request as MessageShape<typeof backend.confirmPairing.input>;
        this.pairingCalls.push(`ConfirmPairing ${String(r.accept && this.pairSays)}`);
        return reply(backend.confirmPairing, {});
      }
      case "RemoveDevice": {
        const r = request as MessageShape<typeof backend.removeDevice.input>;
        if (!this.pairSays) {
          throw new Error(`kith: ${r.id} stays paired`);
        }
        this.pairingCalls.push(`RemoveDevice ${r.id}`);
        this.paired = this.paired?.filter((d) => d.id !== r.id);
        return reply(backend.removeDevice, {});
      }
      case "SignOut": {
        const r = request as MessageShape<typeof backend.signOut.input>;
        this.signedOut.push(`${r.spaceId}${r.forget ? " forget" : ""}`);
        return reply(backend.signOut, {});
      }
      case "CheckSpelling": {
        if (this.spellOff) {
          throw new Error("[failed_precondition] matrix: nothing to check spelling with");
        }
        return reply(backend.checkSpelling, {
          misspellings: checked((request as MessageShape<typeof backend.checkSpelling.input>).text, this.misspelled),
        });
      }
      case "LearnWord":
      case "AllowRareWord": {
        const r = request as MessageShape<typeof backend.learnWord.input>;
        this.learned.push(`${name} ${r.word}${r.forever ? " forever" : ""}`);
        this.misspelled.delete(r.word);
        return reply(backend.learnWord, {});
      }
      case "CompleteWord": {
        const r = request as MessageShape<typeof backend.completeWord.input>;
        const candidates = [...this.vocabulary]
          .filter(([word]) => word.startsWith(r.prefix))
          .sort(([a, x], [b, y]) => y - x || (a < b ? -1 : 1))
          .slice(0, Number(r.limit))
          .map(([word, score]) => ({ word, score: BigInt(score) }));
        return reply(backend.completeWord, { candidates });
      }
      case "ModelTask":
        return reply(backend.modelTask, this.model);
      case "DetectLanguages":
        return reply(backend.detectLanguages, this.languages);
      case "DetectModel":
        return reply(backend.detectModel, this.modelOffer);
      case "InstallDictionary":
      case "InstallFrequencies":
      case "InstallModel":
        this.installed.push(`${name} ${(request as MessageShape<typeof backend.installModel.input>).tag}`);
        return reply(backend.installModel, {});
      case "People": {
        const words = (request as MessageShape<typeof backend.people.input>).query
          .toLowerCase()
          .split(/\s+/)
          .filter((w) => w !== "");
        const people = [...this.cards.entries()].sort(([a], [b]) => (a < b ? -1 : 1));
        return reply(backend.people, {
          people: people.flatMap(([card, c]) => {
            const matched: { key: string; values: string[] }[] = [];
            for (const w of words) {
              const at = w.indexOf(":");
              if (at > 0 && at < w.length - 1) {
                const [key, part] = [w.slice(0, at), w.slice(at + 1)];
                const values = c.fields[key] ?? [];
                if (!values.some((v) => v.toLowerCase().includes(part))) {
                  return [];
                }
                matched.push({ key, values });
              } else if (!card.toLowerCase().includes(w)) {
                return [];
              }
            }
            return [{ card, aliases: c.fields.aliases ?? [], idKeys: Object.keys(c.ids ?? {}), matched }];
          }),
        });
      }
      case "PersonCard": {
        const r = request as MessageShape<typeof backend.personCard.input>;
        // A DM's one person is the person of the card whose conversation it is.
        const dm = [...this.cards.values()].find((c) => r.roomId !== "" && c.conversation === r.roomId);
        if (dm !== undefined) {
          r.person = dm.people[0] ?? "";
        }
        const name = this.cardOf(r.person, r.card);
        return reply(backend.personCard, {
          card: this.shownCard(name),
          person: r.person,
          people: this.cards.get(name)?.people ?? (r.person === "" ? [] : [r.person]),
          conversation: this.cards.get(name)?.conversation ?? "",
        });
      }
      case "SetPersonField": {
        const r = request as MessageShape<typeof backend.setPersonField.input>;
        let name = this.cardOf(r.person, r.card);
        const card = this.cards.get(name);
        if ((card === undefined ? "" : String(card.revision)) !== r.revision) {
          throw new Error("[aborted] phonebook: the card changed since it was read");
        }
        if (card === undefined) {
          name = r.name;
          this.cards.set(name, { people: [r.person], fields: {}, revision: 0, conversation: "" });
        }
        const now = this.cards.get(name);
        if (now !== undefined) {
          now.fields[r.key] = r.values;
          now.revision++;
        }
        this.cardWrites.push(`${name} ${r.key}=${r.values.join(",")}:${profile}`);
        return reply(backend.setPersonField, { card: this.shownCard(name) });
      }
      case "ConfigSettings":
        return reply(backend.configSettings, {
          revision: String(this.configRevision),
          settings: this.configSettings.map((x) => clone(ConfigSettingSchema, x)),
        });
      case "SetConfigSetting": {
        const r = request as MessageShape<typeof backend.setConfigSetting.input>;
        const before = this.beforeConfigWrite;
        this.beforeConfigWrite = undefined;
        before?.();
        const setting = this.configSettings.find((x) => x.path === r.path);
        if (setting === undefined) {
          throw new Error(`[permission_denied] daemon: "${r.path}" is not a setting set one at a time here`);
        }
        if (r.baseRevision !== String(this.configRevision)) {
          throw new Error("[aborted] daemon: the config file changed since it was read");
        }
        if (this.configRefusal !== "") {
          throw new Error(`[invalid_argument] ${this.configRefusal}`);
        }
        if (!same(setting.value, r.value)) {
          setting.value = r.value;
          this.configSets.push(`${r.path}=${words(r.value)}:${profile}`);
          this.configChanged();
        }
        return reply(backend.setConfigSetting, {
          revision: String(this.configRevision),
          setting: clone(ConfigSettingSchema, setting),
        });
      }
      case "PersonKeys":
        return reply(backend.personKeys, {
          keys: [...new Set([...this.cards.values()].flatMap((c) => Object.keys(c.fields)))].sort(),
        });
      case "FilingPlaces": {
        const r = request as MessageShape<typeof backend.filingPlaces.input>;
        if (!p.rooms.some((x) => x.id === r.roomId)) {
          throw new Error(`[not_found] daemon: the profile ${profile} holds no room ${r.roomId}`);
        }
        this.filingSpacesAsked.push(r.filingSpaces);
        const unmatched = r.filingSpaces.includes("space:Gone");
        return reply(backend.filingPlaces, { places: unmatched ? [] : (this.places.get(r.roomId) ?? []), unmatched });
      }
      case "FileRoom": {
        const r = request as MessageShape<typeof backend.fileRoom.input>;
        if (!p.rooms.some((x) => x.id === r.roomId)) {
          throw new Error(`[not_found] daemon: the profile ${profile} holds no room ${r.roomId}`);
        }
        const asked = [
          ...r.tags.map((t) => `${t.tag}${t.in ? "+" : "-"}`),
          ...r.spaces.map((sp) => `${sp.spaceId}${sp.in ? "+" : "-"}`),
        ];
        this.filed.push(`${r.roomId} ${asked.join(" ")}:${profile}`);
        if (this.fileRefusal !== "") {
          throw new Error(this.fileRefusal);
        }
        return reply(backend.fileRoom, this.fileAnswer);
      }
      default:
        throw new Error(`the fake does not answer ${name}`);
    }
  }

  /** theme is the theme as the relay resolves it: this device's value over the shared one. */
  theme(): ThemeResponse {
    const theme = clone(ThemeResponseSchema, this.themeDefaults);
    for (const key of new Set([...this.themeShared.keys(), ...this.themeDevice.keys()])) {
      const shared = this.themeShared.get(key) ?? "";
      const device = this.themeDevice.get(key) ?? "";
      theme.values[key] = create(ThemeValueSchema, {
        value: device || shared,
        source: device ? ThemeSource.DEVICE : shared ? ThemeSource.SHARED : ThemeSource.UNSPECIFIED,
        shared,
        device,
      });
    }
    theme.configRevision = String(this.configRevision);
    theme.deviceRevision = String(this.settingsRevision);
    return theme;
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
      dropped: false,
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
      sub.dropped = true; // the window closed its port: nothing more arrives
      this.close(sub);
    };
  }

  /** attach opens sub at kithd: attached, then what a Seat answers. */
  private attach(sub: Sub): void {
    this.post(sub, { kind: "attached" });
    if (sub.method === backend.startPairing.name) {
      for (const frame of this.pairing) {
        this.send1(sub, backend.startPairing, frame);
      }
    }
    if (sub.method === DeviceService.method.joinBridge.name) {
      for (const frame of this.joining) {
        this.send1(sub, DeviceService.method.joinBridge, frame);
      }
    }
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

  /**
   * post delivers an event later, as a port does: none once the stream is closed, and
   * none posted before once the window closed its port.
   */
  private post(sub: Sub, event: StreamEvent, arrived?: () => void): void {
    if (sub.closed) {
      return;
    }
    void later().then(() => {
      if (!sub.dropped) {
        sub.listener(event);
        arrived?.();
      }
    });
  }

  /**
   * follow is kithd handing profile's window a link to uri (Follow): on its link stream
   * there, with the chats uri names among the profile's rooms (named by id, a room's own
   * ID or "room/event"): the frame sent, or undefined when no link stream is open
   * there (kithd hands it to another window).
   */
  follow(profile: string, uri: string): FollowStreamResponse | undefined {
    const p = this.of(profile);
    const [room = "", event = ""] = uri.split("/");
    const chats = p.rooms.some((r) => r.id === room) ? [{ roomId: room, eventId: event }] : [];
    const open = this.open(backend.followStream.name).filter((s) => s.profile === profile);
    const frame = create(backend.followStream.output, { uri, chats });
    for (const s of open) {
      this.post(s, { kind: "frames", frames: [toBinary(backend.followStream.output, frame)] }, () => {
        this.linksArrived.push(frame);
      });
    }
    return open.length > 0 ? frame : undefined;
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

/**
 * checked is text checked as kithd's engine checks it: each run of letters (apostrophes
 * inside) it rejects, in UTF-8 byte offsets, with its suggestions. Unlike kithd it checks
 * a word with digits too, so a test's numbered words can be marked.
 */
export function checked(text: string, misspelled: ReadonlyMap<string, readonly string[]>) {
  const bytes = new TextEncoder();
  const out: Misspelling[] = [];
  for (const m of text.matchAll(/[\p{L}\p{N}]+(?:'\p{L}+)*/gu)) {
    const suggestions = misspelled.get(m[0]);
    if (suggestions !== undefined) {
      const start = bytes.encode(text.slice(0, m.index)).length;
      const end = start + bytes.encode(m[0]).length;
      out.push(
        create(MisspellingSchema, {
          word: m[0],
          start: BigInt(start),
          end: BigInt(end),
          suggestions: [...suggestions],
        }),
      );
    }
  }
  return out;
}
