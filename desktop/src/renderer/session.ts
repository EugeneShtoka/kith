// The window's state, outside React: the profile it runs in, its seat there, and the
// profile's rooms, unread and invitations, kept by the streams main holds open. React
// reads it through useSyncExternalStore; nothing here draws.
import type { DescMessage, DescMethodServerStreaming, MessageInitShape, MessageShape } from "@bufbuild/protobuf";

import {
  BackendService,
  Change,
  type FollowStreamResponse,
  type RailResponse,
  type RailRow,
  type ReactionsResponse,
  type SeatHolder,
  type SeatResponse,
  type StatusResponse,
} from "../gen/backend/v1/services_pb";
import type { Member, Message, Reaction, Room, SearchHit, Unread } from "../gen/backend/v1/types_pb";
import { type DeviceSettings, DeviceService, type ThemeResponse } from "../gen/device/v1/device_pb";
import { runIn } from "../shared/profile";
import { later, listed, placed, refOf, type RowRef } from "./rail";
import { Composer, type ComposerView, type Unsent } from "./compose";
import { arrived, type Jumplist, nowhere, step } from "./jumplist";
import { call, subscribe } from "./kith";
import type { Asked } from "./polls";
import { changeTheme, type ThemeRelay, themeLabel, type Scope, type Slots } from "./theme";
import { announces } from "./read";
import { ordered, total } from "./rooms";
import { emptyFilter, parseSearch } from "./search";
import { change, type Device, type Key, labels, type Read, withKey } from "./settings";
import { OpenThread, type ThreadView } from "./threads";
import { type Ask, newest, Timeline, type TimelineView } from "./timeline";

const backend = BackendService.method;

/** How many messages a network page asks for, and a cached page holds (as the TUI's). */
const networkPage = 50n;
const cachedPage = 2000n;
/** How many reply targets are fetched at once. */
const quoteFetches = 6;
/** How many hits a search lists, newest first (as the TUI's). */
const searchLimit = 200n;
/** How many replies a page of a thread holds, and how often the window says it is in use, at most. */
const threadPage = 50n;
const presentEvery = 2000;
/**
 * How long the window waits to read the device again after it could not: on a device
 * that joined a bridge, the profiles are the bridge's, read through it, and a window
 * may start before the bridge answers.
 */
export const deviceAgain = 1000;

/** The window's seat in its profile: one window per profile is served (kithd's Seat). */
export type Seat =
  /** No profile yet: nothing to sit in. */
  | { state: "none" }
  /** Asked for, not answered yet. */
  | { state: "asking" }
  | { state: "seated" }
  /** Another window has it; this one may take it over. */
  | { state: "taken"; holder: SeatHolder | undefined }
  /** Another window took it over; this one may take it back. */
  | { state: "steppedAside" };

/** The window's link to kithd: its Changed stream, held open by main. */
export type Link = { state: "connecting" } | { state: "attached" } | { state: "detached"; error: string };

/** Where a search looks: the open room, the rooms the rail row lists, or every room of the profile. */
export type SearchScope = "room" | "row" | "all";

/** A search and its hits, newest first. */
export interface Search {
  query: string;
  scope: SearchScope;
  hits: readonly SearchHit[];
  /** Asked for, not answered yet. */
  busy: boolean;
  /** Why the query asks nothing of kithd ("" it does). */
  note: string;
}

/** View is everything the window draws. */
export interface View {
  /** The config's profiles, in order, and the one the window runs in: "" asks which. */
  profiles: readonly string[];
  profile: string;
  /** Where kithd is: the local one's socket, or the bridge this device joined (one is ""). */
  socket: string;
  bridge: string;
  seat: Seat;
  link: Link;
  status: StatusResponse | undefined;
  /** The rail's rows (Rail), and the one the window is on (-1: none, no rail yet). */
  rail: readonly RailRow[];
  row: number;
  /** The joined rooms the row lists, in list order (every one before a rail), and the invitations. */
  rooms: readonly Room[];
  /** Every joined room of the profile, in list order: what the switcher offers. */
  joined: readonly Room[];
  invites: readonly Room[];
  unread: ReadonlyMap<string, Unread>;
  /** What the tray counts. */
  unreadTotal: number;
  /** The room open ("" none), and its timeline. */
  open: string;
  timeline: TimelineView | undefined;
  /** Whether the unread line is drawn ([display] unread_line). */
  unreadLine: boolean;
  /** The open room's composer. */
  composer: ComposerView | undefined;
  /** The search shown, its hits only in the profile's rooms. */
  search: Search | undefined;
  /** The thread open beside the open room, which the composer writes into. */
  thread: ThreadView | undefined;
  /** Whether there is a room to go back, or forward, to. */
  back: boolean;
  forward: boolean;
  /** The settings page, shown in place of the rest; undefined closed. */
  settingsPage: SettingsPage | undefined;
  /** The theme as last read (Theme); undefined until one is: the built-in palette. */
  theme: ThemeResponse | undefined;
  /** A change of the theme being written, and what the last one says (gave way, or failed). */
  themeChange: { busy: boolean; note: string };
  /** The open room's remembered sender colors (SenderSlots). */
  slots: Slots;
  problem: string | undefined;
}

/** The settings page: the device's settings, each change written to device.toml at once. */
export interface SettingsPage {
  /** The settings as last read or written; undefined while read, or when they could not be. */
  read: Read | undefined;
  /** A change being written: the page takes no other meanwhile. */
  busy: boolean;
  /** What the page says: a change that gave way to another writer's, or a failure. */
  note: string;
}

/** relay is device.toml's settings through the relay, as the settings page changes them. */
const relay: Device = {
  read: () => call(DeviceService.method.deviceSettings, {}).then(answered),
  write: (base, settings) =>
    call(DeviceService.method.updateDeviceSettings, { baseRevision: base, settings }).then(answered),
};

/** themeRelay is the theme through the relay, as the settings page changes it. */
const themeRelay: ThemeRelay = {
  read: () => call(DeviceService.method.theme, {}),
  write: async (base, key, scope, value) => {
    const r = await call(DeviceService.method.updateTheme, {
      configRevision: base.configRevision,
      deviceRevision: base.deviceRevision,
      writes: [{ key, value, scope }],
    });
    if (r.theme === undefined) {
      throw new Error("the relay answered no theme");
    }
    return r.theme;
  },
};

/** errorText is what the settings page says of a failure. */
function errorText(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

/** answered is a DeviceSettings or UpdateDeviceSettings answer, read. */
function answered(r: { settings?: DeviceSettings; revision: string }): Read {
  if (r.settings === undefined) {
    throw new Error("the relay answered no settings");
  }
  return { settings: r.settings, revision: r.revision };
}

const nothingShown = {
  seat: { state: "none" },
  link: { state: "connecting" },
  status: undefined,
  rail: [],
  row: -1,
  rooms: [],
  joined: [],
  search: undefined,
  thread: undefined,
  back: false,
  forward: false,
  invites: [],
  unread: new Map(),
  unreadTotal: 0,
  open: "",
  timeline: undefined,
  unreadLine: true,
  composer: undefined,
  slots: new Map(),
} satisfies Partial<View>;

/** Run is one stay in one profile's seat: what it opened, closed together when it ends. */
interface Run {
  profile: string;
  closed: boolean;
  /** The seat's stream, and the profile's streams once seated. */
  seat: (() => void) | undefined;
  streams: (() => void)[];
  /** Rooms as Rooms last read them, and when each last had a message. */
  joined: readonly Room[];
  at: Readonly<Record<string, bigint>>;
  /** Counts the room reads, so an older answer arriving last is dropped; and whether one has answered. */
  roomReads: number;
  listed: boolean;
  /** A room asked to open before the rooms were read: opened once they are, if held. */
  opening: string | undefined;
  /** The rail served last (the highest revision), and the row the window is on. */
  rail: RailResponse | undefined;
  row: RowRef | undefined;
  rowAt: number;
  /** The Messages stream has said where it starts: a page read from now misses no change. */
  positioned: boolean;
  /** The open room's timeline, and the device settings it follows: undefined while read. */
  timeline: Timeline | undefined;
  settings: DeviceSettings | "failed" | undefined;
  /** Counts the reactions reads; the Reactions frames held while one is answered. */
  reactionReads: number;
  reactionsHeld: ReactionsResponse[] | undefined;
  /** The composer: drafts, files, failed sends. */
  composer: Composer;
  /** The search shown, and how many were asked for: only the last one's answer is shown. */
  search: Search | undefined;
  searches: number;
  /** The thread open in the open room. */
  thread: OpenThread | undefined;
  /** The stream of links kithd hands this window (FollowStream). */
  follows: (() => void) | undefined;
}

export class Session {
  private view: View = {
    profiles: [],
    profile: "",
    socket: "",
    bridge: "",
    settingsPage: undefined,
    theme: undefined,
    themeChange: { busy: false, note: "" },
    problem: undefined,
    ...nothingShown,
  };
  private readonly listeners = new Set<() => void>();
  private run: Run | undefined;
  /** This window, to kithd's seats; and since when it runs, for one it refuses. */
  private readonly client = crypto.randomUUID();
  private readonly since = BigInt(Date.now());
  /**
   * The DeviceService calls, one after another: main moves to the profile each answer
   * leaves it in (runIn), and so does the window, so both read them in one order.
   */
  private device: Promise<void> = Promise.resolve();
  /** The device read again after one failed, while it waits; none once stopped. */
  private deviceLater: ReturnType<typeof setTimeout> | undefined;
  private stopped = false;
  private told = 0;
  /** The open room is watched: shown at its newest, the window focused. */
  private watching = false;
  /**
   * Where the window has been, per profile: back and forward stay in the profile the
   * window runs in, and find their way again when it comes back to one.
   */
  private readonly places = new Map<string, Jumplist>();
  /**
   * The last link handed to the window, until the rooms of the profile it runs in are
   * read; and the link streams of profiles it left, which main ends (their last links
   * still arrive).
   */
  private link: FollowStreamResponse | undefined;
  private readonly lingering = new Set<() => void>();
  /** Profile switches asked for and not landed yet. */
  private switching = 0;
  /** The open thread is watched: its pane shown, the window focused. */
  private watchingThread = false;
  /** Counts the settings page's reads and writes: only the last one's answer is shown. */
  private settingsReads = 0;
  /** Counts the theme's reads and writes, likewise. */
  private themeReads = 0;
  /** What the window last told kithd it has open, and when. */
  private presented = { room: "", at: 0 };

  /** start reads the device, which runs the window in a profile or has it ask. */
  start(): void {
    this.readDevice();
    // Closing, the window writes its drafts first (main waits a little for it).
    window.kith.closing(() => this.run?.composer.drafts.flush() ?? Promise.resolve());
  }

  /** stop closes every stream: the window is going. */
  stop(): void {
    this.stopped = true;
    clearTimeout(this.deviceLater);
    this.leave();
    for (const stop of this.lingering) {
      stop();
    }
    this.lingering.clear();
  }

  readonly subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };

  readonly snapshot = (): View => this.view;

  /** choose runs the window in the profile named, remembered for its next start. */
  choose(name: string): Promise<void> {
    this.switching++; // a link handed meanwhile waits for the profile the window ends in
    return this.inTurn(async () => {
      try {
        const chosen = await call(DeviceService.method.chooseProfile, { name });
        this.enter(chosen.profile);
      } finally {
        this.switching--;
        this.followLink();
      }
    });
  }

  /** openSettings shows the settings page, the settings read afresh. */
  openSettings(): void {
    const mine = ++this.settingsReads;
    this.set({ settingsPage: { read: undefined, busy: false, note: "" }, themeChange: { busy: false, note: "" } });
    this.readTheme(); // device.toml may have been changed since, by the TUI or by hand
    relay.read().then(
      (read) => {
        if (mine === this.settingsReads) {
          this.set({ settingsPage: { read, busy: false, note: "" } });
        }
      },
      (err: unknown) => {
        if (mine === this.settingsReads) {
          this.set({ settingsPage: { read: undefined, busy: false, note: errorText(err) } });
        }
      },
    );
  }

  /** closeSettings goes back from the settings page; an answer still on its way is dropped. */
  closeSettings(): void {
    this.settingsReads++;
    this.set({ settingsPage: undefined });
  }

  /**
   * changeSetting writes one setting of the page's to device.toml at once, onto the file
   * as another writer may have left it meanwhile (settings.change). The page shows the
   * file as it then is, and the open room follows it: its receipts, unread line and
   * composer.
   */
  async changeSetting<K extends Key>(key: K, value: DeviceSettings[K]): Promise<void> {
    const page = this.view.settingsPage;
    if (page?.read === undefined || page.busy) {
      return;
    }
    const shown = page.read;
    const mine = ++this.settingsReads;
    this.set({
      settingsPage: { read: { ...shown, settings: withKey(shown.settings, key, value) }, busy: true, note: "" },
    });
    let read = shown;
    let note = "";
    try {
      const got = await change(relay, shown, key, value);
      read = got;
      if (got.kept) {
        note = `${labels[key]} was changed elsewhere meanwhile: the page shows it as it is now.`;
      }
      const run = this.run;
      if (run?.timeline !== undefined) {
        run.settings = got.settings;
        this.set({ unreadLine: got.settings.unreadLine });
      }
    } catch (err) {
      note = `${labels[key]} was not saved: ${errorText(err)}`;
    }
    if (mine === this.settingsReads) {
      this.set({ settingsPage: { read, busy: false, note } });
    }
  }

  /**
   * changeTheme writes one key of the theme to scope's file (the shared config, or
   * device.toml), onto the files as another writer may have left them meanwhile
   * (theme.changeTheme). The window then draws the theme as it is.
   */
  async changeTheme(key: string, scope: Scope, value: string): Promise<void> {
    const shown = this.view.theme;
    if (shown === undefined || this.view.themeChange.busy) {
      return;
    }
    const mine = ++this.themeReads;
    this.set({ themeChange: { busy: true, note: "" } });
    let note = "";
    try {
      const got = await changeTheme(themeRelay, shown, key, scope, value);
      if (mine === this.themeReads) {
        this.set({ theme: got.theme });
      }
      if (got.kept) {
        note = `${themeLabel(key)} was changed elsewhere meanwhile: it shows as it is now.`;
      }
    } catch (err) {
      note = `${themeLabel(key)} was not saved: ${errorText(err)}`;
    }
    this.set({ themeChange: { busy: false, note } });
  }

  /** readTheme reads the theme again; one that cannot be read leaves the last one drawn. */
  private readTheme(): void {
    const mine = ++this.themeReads;
    themeRelay.read().then(
      (theme) => {
        if (mine === this.themeReads && !this.stopped) {
          this.set({ theme });
        }
      },
      () => undefined, // kithd away, or a [gui.theme] that does not check: the last theme stays
    );
  }

  /** readSlots reads the room's remembered sender colors, which its people are drawn in until picked. */
  private readSlots(run: Run, room: string): void {
    call(backend.senderSlots, { roomId: room }).then(
      (r) => {
        if (this.live(run) && this.view.open === room) {
          this.set({ slots: new Map(r.slots.map((s) => [s.key, s.slot])) });
        }
      },
      () => undefined, // drawn in the window's own hues
    );
  }

  /** takeOver asks for the profile's seat by force: the window that has it steps aside. */
  takeOver(): void {
    const profile = this.view.profile;
    if (profile === "" || this.view.seat.state === "seated") {
      return;
    }
    this.leave();
    this.sit(this.begin(profile), true);
  }

  /** accept joins the room an invitation is to. */
  accept(room: string): Promise<void> {
    return this.inRun(async (run) => {
      await call(backend.joinRoom, { roomIdOrAlias: room });
      this.readRooms(run);
    });
  }

  /** decline leaves the room an invitation is to, as the TUI does. */
  decline(room: string): Promise<void> {
    return this.inRun(async () => {
      await call(backend.leaveRoom, { roomId: room });
    });
  }

  /**
   * open shows a room of the profile, picked (the room list, the switcher): back returns
   * to where the window was.
   */
  open(room: string): void {
    const run = this.run;
    if (run !== undefined && !run.listed) {
      run.opening = room; // opened once the rooms are read, if the profile holds it
      return;
    }
    if (run === undefined || !this.holds(run, room)) {
      return;
    }
    this.places.set(run.profile, arrived(this.places.get(run.profile) ?? nowhere, room));
    this.show(run, room);
  }

  /** back opens the room the window was in before, of those still in the profile. */
  back(): void {
    this.step(true);
  }

  /** forward opens the room back left. */
  forward(): void {
    this.step(false);
  }

  /**
   * search asks for the messages query finds in scope, in the profile's rooms; only the
   * last search asked is shown. The query's filters are read as the TUI reads them.
   */
  search(query: string, scope: SearchScope): void {
    const run = this.run;
    if (run === undefined) {
      return;
    }
    const mine = ++run.searches;
    const filter = parseSearch(query, new Date());
    const shown = (s: Omit<Search, "query" | "scope">) => {
      run.search = { query, scope, ...s };
      this.set({});
    };
    if (filter.senderField !== "") {
      shown({
        hits: [],
        busy: false,
        note: `from:${filter.senderField} names a phonebook field: search it in the TUI`,
      });
      return;
    }
    if (emptyFilter(filter) || (scope === "room" && this.view.open === "")) {
      shown({ hits: [], busy: false, note: emptyFilter(filter) ? "Nothing to search for" : "No room is open" });
      return;
    }
    shown({ hits: [], busy: true, note: "" });
    const ms = (at: Date | undefined) => BigInt(at?.getTime() ?? 0);
    call(backend.searchMessages, {
      query: filter.terms,
      sender: filter.sender,
      since: ms(filter.since),
      until: ms(filter.until),
      limit: searchLimit,
      roomIds: scope === "room" ? [this.view.open] : scope === "row" ? this.view.rooms.map((r) => r.id) : [],
      allRooms: scope === "all",
    }).then(
      (r) => {
        if (this.live(run) && mine === run.searches) {
          shown({ hits: r.hits, busy: false, note: "" });
        }
      },
      (err: unknown) => {
        if (this.live(run) && mine === run.searches) {
          shown({ hits: [], busy: false, note: "" });
          this.fail(err);
        }
      },
    );
  }

  /** closeSearch puts the search away; an answer still on its way is dropped. */
  closeSearch(): void {
    if (this.run !== undefined) {
      this.run.searches++;
      this.run.search = undefined;
      this.set({});
    }
  }

  /**
   * jump opens the room a message is in and reads back to the message, shown and marked
   * once found; a room the profile no longer holds is not opened.
   */
  jump(room: string, event: string): void {
    const run = this.run;
    if (run === undefined) {
      return;
    }
    if (!this.holds(run, room)) {
      this.fail("That room is no longer in this profile");
      return;
    }
    this.open(room);
    run.timeline?.seek(event);
    this.sought(run);
    this.pump(run);
    this.set({});
  }

  /** sought says so when the open room's jump found nothing, and opens the thread it found a reply in. */
  private sought(run: Run): void {
    if (run.timeline?.jumpMissed() === true) {
      this.fail("That message is no longer cached");
    }
    const root = run.timeline?.jumpThread() ?? "";
    if (root !== "") {
      this.openThread(root);
    }
  }

  /** openThread opens the thread root of the open room beside it: the composer writes into it. */
  openThread(root: string): void {
    const run = this.run;
    const room = this.view.open;
    if (run === undefined || room === "" || run.thread?.root === root) {
      return;
    }
    run.thread = new OpenThread(room, root);
    this.pumpThread(run);
    this.markThreadRead(run);
    this.set({});
  }

  /** closeThread puts the thread pane away: the composer writes into the room again. */
  closeThread(): void {
    const run = this.run;
    const t = run?.thread;
    if (run === undefined || t === undefined) {
      return;
    }
    run.thread = undefined;
    this.set({});
  }

  /** olderInThread asks for the open thread's page of replies before those shown. */
  olderInThread(): void {
    const run = this.run;
    if (run?.thread !== undefined) {
      run.thread.older();
      this.pumpThread(run);
      this.set({});
    }
  }

  /** watchThread says whether the open thread is watched (its pane shown, the window focused). */
  watchThread(watching: boolean): void {
    this.watchingThread = watching;
    if (this.run !== undefined) {
      this.markThreadRead(this.run);
    }
  }

  /** holds: the profile's joined rooms, as last read, have room. */
  private holds(run: Run, room: string): boolean {
    return run.joined.some((r) => r.id === room && r.membership !== "invite");
  }

  private step(back: boolean): void {
    const run = this.run;
    if (run === undefined || !run.listed) {
      return; // until the rooms are read, every place would look gone
    }
    const { j, to } = step(this.places.get(run.profile) ?? nowhere, back, (room) => this.holds(run, room));
    this.places.set(run.profile, j);
    if (to === "") {
      this.set({});
    } else {
      this.show(run, to);
    }
  }

  /**
   * show shows a room: its newest cached messages, then older ones as the window scrolls
   * back, kept by the Messages stream. The unread line follows the last message read as
   * it opened, and stays there while it is open.
   */
  private show(run: Run, room: string): void {
    if (room === this.view.open) {
      return;
    }
    this.closeThread();
    run.timeline = new Timeline(room, this.view.unread.get(room)?.readEvent ?? "");
    run.settings = undefined;
    run.composer.opened(room);
    this.set({ open: room, unreadLine: true, slots: new Map() });
    this.readSettings(run, run.timeline);
    this.readSlots(run, room);
    this.readReactions(run);
    this.pump(run);
    this.present(run);
  }

  /**
   * present tells kithd the room open, which a clicked link is handed by (to the window
   * that has its room open): at once for a room opened, at most every presentEvery else.
   */
  private present(run: Run): void {
    const room = this.view.open;
    const now = Date.now();
    if (room === this.presented.room && now - this.presented.at < presentEvery) {
      return;
    }
    this.presented = { room, at: now };
    call(backend.present, { client: this.client, openRoomId: room }).catch((err: unknown) => {
      if (this.live(run)) {
        this.fail(err);
      }
    });
  }

  /** older asks for the open room's page before what is shown. */
  older(): void {
    const run = this.run;
    if (run?.timeline !== undefined) {
      run.timeline.older();
      this.pump(run);
    }
  }

  /** watch says whether the open room is watched (at its newest, the window focused): what arrives meanwhile is read. */
  watch(watching: boolean): void {
    const was = this.watching;
    this.watching = watching;
    if (this.run !== undefined) {
      this.markRead(this.run);
      if (watching) {
        this.present(this.run);
      }
      if (was && !watching) {
        void this.run.composer.drafts.flush(0); // out of sight: the drafts are written now
      }
    }
  }

  /** type is the person typing in the open room's composer; it answers where the caret goes. */
  type(text: string, caret: number): number {
    return this.run === undefined || this.view.open === ""
      ? caret
      : this.run.composer.type(this.view.open, text, caret);
  }

  /** mention puts m in place of the open composer's text[start, end); it answers the caret. */
  mention(m: Member, start: number, end: number): number {
    return this.run === undefined ? end : this.run.composer.mention(this.view.open, m, start, end);
  }

  /** emoji records an emoji put in the open composer. */
  emoji(emoji: string): void {
    this.run?.composer.emoji(this.view.open, emoji);
  }

  /** wantMembers reads the open room's people for mentions. */
  wantMembers(): void {
    this.run?.composer.wantMembers(this.view.open);
  }

  reply(m: Message): void {
    this.run?.composer.reply(m);
  }

  edit(m: Message): void {
    this.run?.composer.edit(m);
  }

  /** cancel ends the open composer's correction, or its reply. */
  cancel(): void {
    this.run?.composer.cancel(this.view.open);
  }

  remove(m: Message): Promise<void> {
    return this.inRun((run) => run.composer.remove(m));
  }

  /** react toggles key on m. */
  react(m: Message, key: string): Promise<void> {
    return this.inRun((run) => run.composer.react(m, key, run.timeline?.view().reactions.get(m.id) ?? []));
  }

  /** vote votes in m's poll by answer IDs; none takes the vote back. */
  vote(m: Message, options: readonly string[]): Promise<void> {
    return this.inRun((run) => run.composer.vote(m, options));
  }

  /** showVoters reads who chose what in m's poll, and shows it. */
  showVoters(m: Message): void {
    this.run?.composer.showVoters(m);
  }

  closeVoters(): void {
    this.run?.composer.closeVoters();
  }

  /** askPoll asks a poll in the open room; it answers whether it was asked. */
  askPoll(poll: Asked): Promise<boolean> {
    return this.run?.composer.ask(this.view.open, poll) ?? Promise.resolve(false);
  }

  /** pick offers files picked in the open dialog to the open room's next message. */
  pick(): Promise<void> {
    const room = this.view.open;
    return this.inRun(async (run) => {
      run.composer.offer(room, await window.kith.pick());
    });
  }

  /** offer offers files dropped or pasted. */
  offer(files: readonly File[]): Promise<void> {
    const room = this.view.open;
    return this.inRun(async (run) => {
      run.composer.offer(room, await Promise.all(files.map((f) => window.kith.offer(f))));
    });
  }

  discard(token: string): Promise<void> {
    return this.inRun((run) => run.composer.discard(this.view.open, token));
  }

  /** send sends the open composer. */
  send(): Promise<void> {
    const room = this.view.open;
    return this.inRun((run) => run.composer.send(room));
  }

  /** answer replies to the question before sending as SMS. */
  answer(yes: boolean): Promise<void> {
    return this.inRun((run) => run.composer.answer(yes));
  }

  retry(u: Unsent): Promise<void> {
    return this.inRun((run) => run.composer.retry(u));
  }

  dismiss(u: Unsent): void {
    this.run?.composer.dismiss(u);
  }

  /** save saves m's attachment where the person picks. */
  save(m: Message): Promise<void> {
    return this.inRun(async () => {
      await window.kith.save(m.roomId, m.id, m.media?.name ?? "");
    });
  }

  /**
   * readDevice reads the profiles again and moves the window where the answer leaves it.
   * One that fails is tried again a little later, until one is read.
   */
  private readDevice(): void {
    void this.inTurn(async () => {
      const d = await call(DeviceService.method.device, {}).catch((err: unknown) => {
        if (!this.stopped) {
          this.deviceLater ??= setTimeout(() => {
            this.deviceLater = undefined;
            this.readDevice();
          }, deviceAgain);
        }
        throw err;
      });
      this.set({ profiles: d.profiles, socket: d.socket, bridge: d.bridge, problem: undefined });
      this.readTheme(); // read as the profiles are: at the start, and after each config change
      this.enter(runIn(this.view.profile, d.profile, d.profiles));
    });
  }

  private inTurn(f: () => Promise<void>): Promise<void> {
    this.device = this.device.then(f).catch((err: unknown) => {
      this.fail(err);
    });
    return this.device;
  }

  /**
   * enter runs the window in profile: what the last one showed goes, and its seat is
   * asked for. "" shows the picker. The profile it already runs in is left as it is,
   * seated or not.
   */
  private enter(profile: string): void {
    if (profile === this.view.profile) {
      return;
    }
    // Main ends the last profile's link stream: links it handed meanwhile still arrive.
    this.leave(true);
    this.set({ ...nothingShown, profile });
    if (profile !== "") {
      this.sit(this.begin(profile), false);
    }
  }

  private begin(profile: string): Run {
    const run: Run = {
      profile,
      closed: false,
      seat: undefined,
      streams: [],
      joined: [],
      at: {},
      roomReads: 0,
      listed: false,
      opening: undefined,
      search: undefined,
      searches: 0,
      thread: undefined,
      follows: undefined,
      rail: undefined,
      row: undefined,
      rowAt: 0,
      positioned: false,
      timeline: undefined,
      settings: undefined,
      reactionReads: 0,
      reactionsHeld: undefined,
      composer: new Composer({
        changed: () => {
          if (this.live(run)) {
            this.set({});
          }
        },
        failed: (err) => {
          if (this.live(run)) {
            this.fail(err);
          }
        },
        room: (id) => run.joined.find((r) => r.id === id),
        settings: () => (typeof run.settings === "object" ? run.settings : undefined),
        speakers: (room) => speakers(run.timeline?.room === room ? run.timeline.view().rows : []),
        thread: (room) => (run.thread?.room === room ? run.thread.root : ""),
      }),
    };
    this.run = run;
    return run;
  }

  /**
   * leave closes everything the run opened; its late answers are dropped. Its link
   * stream is left for main to end when the window leaves the profile (lingers), so a
   * link handed to it meanwhile is not lost with it.
   */
  private leave(lingers = false): void {
    const run = this.run;
    this.run = undefined;
    if (run === undefined) {
      return;
    }
    run.closed = true;
    run.composer.stop();
    run.seat?.();
    for (const stop of run.streams) {
      stop();
    }
    if (run.follows !== undefined) {
      if (lingers) {
        this.lingering.add(run.follows);
      } else {
        run.follows();
      }
    }
  }

  private live(run: Run): boolean {
    return !run.closed && run === this.run;
  }

  /**
   * sit asks for the seat, by force or not, and opens the profile's streams once it is
   * granted. Main asks again (never by force) after kithd restarts, so a window that took
   * the seat meanwhile keeps it, and this one is told so.
   */
  private sit(run: Run, force: boolean): void {
    this.set({ seat: { state: "asking" } });
    run.seat = subscribe(backend.seat, { client: this.client, force, where: { sinceUnixMs: this.since } }, (e) => {
      if (!this.live(run)) {
        return;
      }
      if (e.kind === "frames") {
        for (const f of e.frames) {
          this.seatEvent(run, f);
        }
      } else if (e.kind === "refused") {
        this.fail(e.error);
      }
    });
  }

  private seatEvent(run: Run, frame: SeatResponse): void {
    if (!this.live(run)) {
      return; // an earlier frame of the batch ended the run
    }
    switch (frame.event.case) {
      case "granted":
        if (this.view.seat.state !== "seated") {
          this.set({ seat: { state: "seated" } });
          this.follow(run);
          run.composer.start();
        }
        break;
      case "taken":
        this.lose({ state: "taken", holder: frame.event.value });
        break;
      case "stepAside":
        // The drafts are written first, from the seat (kithd refuses them once the other
        // window has it); closing the seat's stream is what lets the other window in.
        void run.composer.drafts.flush().finally(() => {
          if (this.live(run)) {
            this.lose({ state: "steppedAside" });
          }
        });
        break;
      case undefined:
        break;
    }
  }

  /** lose ends the run: its streams stop, its rooms go, and the window says why. */
  private lose(seat: Seat): void {
    this.leave();
    this.set({ ...nothingShown, seat });
  }

  /** follow opens the profile's streams, each read whole as it opens and kept by its frames. */
  private follow(run: Run): void {
    let reopened = false;
    // The links kithd hands this window: whichever run's stream brings one, it is the
    // window's, followed in the profile it runs in once that profile's rooms are read.
    const follows = subscribe(backend.followStream, { client: this.client }, (e) => {
      if (e.kind === "frames") {
        this.link = e.frames.at(-1) ?? this.link; // a later link replaces one not followed yet
        this.followLink();
      } else if (e.kind === "refused") {
        if (!this.lingering.delete(follows) && this.live(run)) {
          this.fail(e.error);
        }
      }
    });
    run.follows = follows;
    run.streams.push(
      subscribe(backend.changed, {}, (e) => {
        if (!this.live(run)) {
          return;
        }
        switch (e.kind) {
          case "attached":
            this.set({ link: { state: "attached" } });
            this.readStatus(run);
            this.readRooms(run);
            this.readRail(run);
            break;
          case "frames":
            if (e.frames.some((f) => f.change === Change.ROOMS)) {
              this.readRooms(run);
            }
            if (e.frames.some((f) => f.change === Change.RAIL)) {
              this.readRail(run);
            }
            if (e.frames.some((f) => f.change === Change.ACCOUNTS)) {
              this.readStatus(run);
            }
            break;
          case "detached":
            this.set({ link: { state: "detached", error: e.error } });
            this.readStatus(run);
            break;
          case "refused":
            this.fail(e.error);
            break;
        }
      }),
      // A config change can add, rename or remove a profile, or change what this one holds;
      // one made while kithd was away comes with no frame, so a reopening reads again too.
      subscribe(backend.configChanged, {}, (e) => {
        if (!this.live(run)) {
          return;
        }
        if (e.kind === "frames" || (e.kind === "attached" && reopened)) {
          this.readDevice();
          this.readRooms(run);
        } else if (e.kind === "attached") {
          this.readTheme(); // kithd may have been away when the window started
        }
        reopened ||= e.kind === "attached";
      }),
    );
    run.streams.push(
      subscribe(backend.messages, {}, (e) => {
        if (!this.live(run)) {
          return;
        }
        switch (e.kind) {
          case "frames":
            for (const f of e.frames) {
              if (f.message !== undefined) {
                run.timeline?.frame(f.message);
                run.thread?.frame(f.message);
              }
            }
            // The first frame says where the stream starts: a page read after it misses
            // nothing, each change after it coming as a frame.
            run.positioned = true;
            this.sought(run);
            this.pump(run);
            this.pumpThread(run);
            this.markThreadRead(run);
            this.markRead(run);
            this.set({});
            break;
          case "reload":
            // kithd could not resume where main left off: frames were lost, so what is
            // shown is read again, and so are the rooms' last messages.
            run.positioned = true;
            run.timeline?.reload();
            run.thread?.reload();
            this.pumpThread(run);
            this.readRooms(run);
            this.pump(run);
            this.set({});
            break;
          case "refused":
            this.fail(e.error);
            break;
          case "attached":
          case "detached":
            break; // main resumes it where it left off
        }
      }),
      // Reactions do not resume: read whole again as the stream opens, frames meanwhile held.
      subscribe(backend.reactions, {}, (e) => {
        if (!this.live(run)) {
          return;
        }
        if (e.kind === "attached") {
          this.readReactions(run);
        } else if (e.kind === "frames") {
          if (run.reactionsHeld !== undefined) {
            run.reactionsHeld.push(...e.frames);
          } else {
            this.reacted(run, e.frames);
          }
        } else if (e.kind === "refused") {
          this.fail(e.error);
        }
      }),
    );
    this.keep(
      run,
      backend.unreadStream,
      {},
      async () => (await call(backend.cachedUnread, {})).unread,
      (all) => {
        this.set({ unread: new Map(all.map((u) => [u.roomId, u])) });
      },
      (frame) => {
        if (frame.unread !== undefined) {
          this.set({ unread: new Map(this.view.unread).set(frame.unread.roomId, frame.unread) });
        }
      },
    );
    this.keep(
      run,
      backend.invites,
      {},
      async () => (await call(backend.cachedInvites, {})).invites,
      (invites) => {
        this.set({ invites });
      },
      (frame) => {
        this.set({ invites: frame.invites });
      },
    );
  }

  /**
   * keep holds a stream whose state is read whole (read) each time it opens, kithd
   * having restarted or not: its frames until that read answers are held, then applied
   * over it in order, so none is lost to an older read and none counted twice (a frame
   * carries a room's whole count, or the whole set).
   */
  private keep<I extends DescMessage, O extends DescMessage, S>(
    run: Run,
    method: DescMethodServerStreaming<I, O>,
    input: MessageInitShape<I>,
    read: () => Promise<S>,
    replace: (state: S) => void,
    apply: (frame: MessageShape<O>) => void,
  ): void {
    let opened = 0;
    let held: MessageShape<O>[] | undefined;
    const settle = (mine: number, state: S | undefined) => {
      if (!this.live(run) || mine !== opened) {
        return; // an older opening's read: the newer one's covers it
      }
      if (state !== undefined) {
        replace(state);
      }
      const frames = held ?? [];
      held = undefined;
      frames.forEach(apply);
    };
    run.streams.push(
      subscribe(method, input, (e) => {
        if (!this.live(run)) {
          return;
        }
        switch (e.kind) {
          case "attached": {
            const mine = ++opened;
            held = [];
            read().then(
              (state) => {
                settle(mine, state);
              },
              (err: unknown) => {
                this.fail(err);
                settle(mine, undefined);
              },
            );
            break;
          }
          case "frames":
            if (held !== undefined) {
              held.push(...e.frames);
            } else {
              e.frames.forEach(apply);
            }
            break;
          case "detached":
            break;
          case "refused":
            this.fail(e.error);
            break;
        }
      }),
    );
  }

  /**
   * pump asks for the open room's next page, one at a time, once the Messages stream has
   * said where it starts; and for the reply targets neither shown nor asked for.
   */
  private pump(run: Run): void {
    const t = run.timeline;
    if (t === undefined || !run.positioned || !this.live(run)) {
      return;
    }
    for (const id of t.unquoted(quoteFetches)) {
      call(backend.fetchEvent, { roomId: t.room, eventId: id }).then(
        (r) => {
          if (r.message !== undefined && this.live(run) && run.timeline === t) {
            t.fetchedQuote(r.message);
            this.set({});
          }
        },
        () => undefined, // the reply keeps its placeholder
      );
    }
    const ticket = t.ask();
    if (ticket === undefined) {
      return;
    }
    page(t.room, ticket.ask).then(
      (p) => {
        if (this.live(run) && run.timeline === t) {
          t.land(ticket, p.messages, p.next, p.reactions);
          this.sought(run);
          this.pumpThread(run); // the root may be a row now
          this.pump(run);
          this.markRead(run);
          this.set({});
        }
      },
      (err: unknown) => {
        if (this.live(run) && run.timeline === t) {
          t.failed(ticket);
          if (ticket.ask.kind !== "network") {
            this.fail(err); // a network's page hands over to the cache's, unsaid
          }
          this.pump(run);
          this.set({});
        }
      },
    );
  }

  /**
   * followLink follows the link handed last, once the rooms of the profile the window
   * runs in are read: to the chat it names there (at its message, if it names one), and
   * to nothing outside it. kithd names the chats in the profile of the stream that
   * brought it; one that came from a profile the window has left opens only if this one
   * holds it too.
   */
  private followLink(): void {
    const run = this.run;
    const link = this.link;
    if (run === undefined || link === undefined || !run.listed || this.switching > 0) {
      return;
    }
    this.link = undefined;
    const chat = link.chats.find((c) => this.holds(run, c.roomId));
    if (chat === undefined) {
      this.fail(`That link names no chat in ${run.profile}`);
    } else if (chat.eventId === "") {
      this.open(chat.roomId);
    } else {
      this.jump(chat.roomId, chat.eventId);
    }
  }

  /** pumpThread asks for the open thread's root, or the page of replies the window asked for. */
  private pumpThread(run: Run): void {
    const t = run.thread;
    if (t === undefined || !run.positioned || !this.live(run)) {
      return;
    }
    const ticket = t.ask(run.timeline?.view().rows ?? []);
    if (ticket === undefined) {
      return;
    }
    const ask = ticket.ask;
    const read =
      ask.kind === "root"
        ? call(backend.fetchEvent, { roomId: t.room, eventId: t.root }).then((r) => ({
            messages: r.message === undefined ? [] : [r.message],
            next: "",
          }))
        : call(backend.threadPage, { roomId: t.room, root: t.root, from: ask.from, limit: threadPage }).then((r) => ({
            messages: r.page?.messages ?? [],
            next: r.page?.next ?? "",
          }));
    read.then(
      (p) => {
        if (this.live(run) && run.thread === t) {
          t.land(ticket, p.messages, p.next);
          this.pumpThread(run);
          this.markThreadRead(run);
          this.set({});
        }
      },
      (err: unknown) => {
        if (this.live(run) && run.thread === t) {
          t.failed(ticket);
          if (ask.kind === "page") {
            this.fail(err); // a root not cached is shown without it, unsaid
          }
          this.set({});
        }
      },
    );
  }

  /**
   * markThreadRead sends the open thread's receipt for its newest reply while the pane
   * is watched, once the settings are read: announced or private as a room's is.
   */
  private markThreadRead(run: Run): void {
    const t = run.thread;
    if (t === undefined || !this.watchingThread || run.settings === undefined) {
      return;
    }
    const last = t.view(run.timeline?.view().rows ?? []).rows.findLast((m) => m.id !== t.root && m.id !== "");
    if (last === undefined || last.id === t.marked || last.id === t.sending) {
      return;
    }
    const settings = run.settings === "failed" ? undefined : run.settings;
    t.sending = last.id;
    call(backend.markThreadRead, {
      roomId: t.room,
      root: t.root,
      eventId: last.id,
      private: !announces(settings, t.room, last.sender),
    }).then(
      () => {
        if (t.sending === last.id) {
          t.sending = "";
          t.marked = last.id;
        }
      },
      (err: unknown) => {
        if (t.sending === last.id) {
          t.sending = "";
        }
        if (this.live(run)) {
          this.fail(err);
        }
      },
    );
  }

  /** readSettings reads the device settings the open room's receipts and unread line follow. */
  private readSettings(run: Run, t: Timeline): void {
    relay.read().then(
      ({ settings }) => {
        if (this.live(run) && run.timeline === t) {
          run.settings = settings;
          this.set({ unreadLine: settings.unreadLine });
          this.markRead(run);
        }
      },
      (err: unknown) => {
        if (this.live(run) && run.timeline === t) {
          run.settings = "failed"; // its receipts are private: nothing says they may be announced
          this.fail(err);
          this.markRead(run);
        }
      },
    );
  }

  /**
   * markRead sends a receipt for the open room's newest message while it is watched,
   * once its settings are read: announced where they say so, else private, which still
   * clears its badge everywhere. Each message is marked once; a failed receipt is sent
   * again with the next.
   */
  private markRead(run: Run): void {
    const t = run.timeline;
    if (t === undefined || !t.loaded || !this.watching || run.settings === undefined) {
      return;
    }
    const last = newest(t.view().rows);
    if (last === undefined || last.id === t.marked || last.id === t.sending) {
      return;
    }
    const settings = run.settings === "failed" ? undefined : run.settings;
    const quiet = !announces(settings, t.room, last.sender);
    t.sending = last.id;
    call(backend.markRead, { roomId: t.room, eventId: last.id, private: quiet }).then(
      () => {
        if (t.sending === last.id) {
          t.sending = "";
          t.marked = last.id;
        }
      },
      (err: unknown) => {
        if (t.sending === last.id) {
          t.sending = "";
        }
        if (this.live(run)) {
          this.fail(err);
        }
      },
    );
  }

  /** readReactions reads the open room's reactions whole, the stream's frames meanwhile held. */
  private readReactions(run: Run): void {
    const t = run.timeline;
    if (t === undefined) {
      return;
    }
    const mine = ++run.reactionReads;
    run.reactionsHeld ??= [];
    const settle = (all: Reaction[] | undefined) => {
      if (!this.live(run) || mine !== run.reactionReads) {
        return; // a newer read covers it
      }
      if (all !== undefined && run.timeline === t) {
        t.reactionsRead(all);
      }
      const held = run.reactionsHeld ?? [];
      run.reactionsHeld = undefined;
      this.reacted(run, held);
    };
    call(backend.cachedReactions, { roomId: t.room }).then(
      (r) => {
        settle(r.reactions);
      },
      (err: unknown) => {
        this.fail(err);
        settle(undefined);
      },
    );
  }

  private reacted(run: Run, frames: readonly ReactionsResponse[]): void {
    for (const f of frames) {
      if (f.update !== undefined) {
        run.timeline?.reacted(f.update);
      }
    }
    this.set({});
  }

  /** readRooms reads the profile's rooms and when each last had a message; the newest read wins. */
  private readRooms(run: Run): void {
    const mine = ++run.roomReads;
    Promise.all([call(backend.rooms, {}), call(backend.lastMessages, {})]).then(
      ([rooms, last]) => {
        if (this.live(run) && mine === run.roomReads) {
          run.joined = rooms.rooms;
          run.listed = true;
          run.at = last.at;
          const opening = run.opening;
          run.opening = undefined;
          if (opening !== undefined) {
            this.open(opening);
          }
          this.followLink();
          this.set({});
        }
      },
      (err: unknown) => {
        if (this.live(run)) {
          this.fail(err);
        }
      },
    );
  }

  /**
   * readRail reads the profile's rail and stays on the row the window was on, or on the
   * row now in its place. An answer for a run that ended (another profile) is dropped, as
   * is one older than the rail held. Refused, the device is read again: a profile gone
   * from the config leaves it, its rail and its rooms, and the window asks for one.
   */
  private readRail(run: Run): void {
    call(backend.rail, {}).then(
      (rail) => {
        if (!this.live(run) || !later(run.rail, rail)) {
          return;
        }
        run.rail = rail;
        run.rowAt = placed(rail.rows, run.row, run.rowAt);
        run.row = refOf(rail.rows[run.rowAt]);
        this.set({});
      },
      (err: unknown) => {
        if (this.live(run)) {
          this.fail(err);
          this.readDevice();
        }
      },
    );
  }

  /** selectRow puts the window on the rail's ith row: the room list is its rooms. */
  selectRow(i: number): void {
    const run = this.run;
    const row = run?.rail?.rows[i];
    if (run === undefined || row === undefined) {
      return;
    }
    run.rowAt = i;
    run.row = refOf(row);
    this.set({});
  }

  private readStatus(run: Run): void {
    call(backend.status, {}).then(
      (status) => {
        if (this.live(run)) {
          this.set({ status, problem: undefined });
        }
      },
      (err: unknown) => {
        if (this.live(run)) {
          this.fail(err);
        }
      },
    );
  }

  private fail(err: unknown): void {
    this.set({ problem: err instanceof Error ? err.message : String(err) });
  }

  /** set changes the view, with the list and its count derived again, and tells who listens. */
  private set(change: Partial<View>): void {
    const next = { ...this.view, ...change };
    const run = this.run;
    const joined = run === undefined ? [] : ordered(run.joined, next.unread, run.at);
    next.rail = run?.rail?.rows ?? [];
    next.row = run?.rail === undefined ? -1 : placed(next.rail, run.row, run.rowAt);
    next.rooms = listed(joined, run?.rail, next.row, next.open);
    next.joined = joined;
    next.unreadTotal = total(joined, next.unread);
    // A hit is listed only while its room is in the profile: one that left since it was
    // found shows nothing of it.
    const search = run?.search;
    const ids = new Set(joined.map((r) => r.id));
    next.search = search && { ...search, hits: search.hits.filter((h) => ids.has(h.roomId)) };
    const places = run === undefined ? nowhere : (this.places.get(run.profile) ?? nowhere);
    next.back = places.back.length > 0;
    next.forward = places.forward.length > 0;
    next.timeline = run?.timeline?.view();
    next.composer = run === undefined || next.open === "" ? undefined : run.composer.view(next.open);
    next.thread = run?.thread?.view(next.timeline?.rows ?? []);
    this.view = next;
    if (next.unreadTotal !== this.told) {
      this.told = next.unreadTotal;
      window.kith.unread(next.unreadTotal);
    }
    for (const listener of this.listeners) {
      listener();
    }
  }

  private inRun(f: (run: Run) => Promise<void>): Promise<void> {
    const run = this.run;
    if (run === undefined) {
      return Promise.resolve();
    }
    return f(run).catch((err: unknown) => {
      if (this.live(run)) {
        this.fail(err);
      }
    });
  }
}

/** speakers are who wrote rows, newest first, each once: the first people a mention offers. */
function speakers(rows: readonly Message[]): string[] {
  return [...new Set(rows.toReversed().map((m) => m.sender))];
}

/** page reads what ask is for in room: its messages, and a network page's token and reactions. */
async function page(room: string, ask: Ask): Promise<{ messages: Message[]; next: string; reactions: Reaction[] }> {
  switch (ask.kind) {
    case "latest": {
      const r = await call(backend.cachedTimeline, { roomId: room });
      return { messages: r.messages, next: "", reactions: [] };
    }
    case "network": {
      const r = await call(backend.timeline, { roomId: room, from: ask.from, limit: networkPage });
      return { messages: r.page?.messages ?? [], next: r.page?.next ?? "", reactions: r.page?.reactions ?? [] };
    }
    case "cached": {
      const r = await call(backend.messagesAround, { roomId: room, eventId: ask.before, before: cachedPage });
      return { messages: r.messages, next: "", reactions: [] };
    }
  }
}
