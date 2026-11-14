// The window's state, outside React: the profile it runs in, and the profile's rooms, unread and invitations, kept by the streams main holds open. React
// reads it through useSyncExternalStore; nothing here draws.
import {
  create,
  type DescMessage,
  type DescMethodServerStreaming,
  type MessageInitShape,
  type MessageShape,
} from "@bufbuild/protobuf";

import {
  BackendService,
  Change,
  type Account,
  type ConfigSettingsResponse,
  type ConfigValue,
  type Device as PairedDevice,
  type FileRoomResponse,
  type FollowStreamResponse,
  type Identity,
  type LoginNetwork,
  type MarkRoomsReadResponse,
  type PersonListed,
  type RailResponse,
  type RailRow,
  RailRowKind,
  type ReactionsResponse,
  type StatusResponse,
} from "../gen/backend/v1/services_pb";
import type { Member, Message, Reaction, Room, SearchHit, ThreadUnread, Unread } from "../gen/backend/v1/types_pb";
import {
  type DeviceSettings,
  DeviceService,
  type Jump,
  JumpSchema,
  type TagKey,
  type ThemeResponse,
} from "../gen/device/v1/device_pb";
import { runIn } from "../shared/profile";
import { later, listed, named, placed, refOf, type RowRef, silencedRooms } from "./rail";
import { Composer, type ComposerView, type Unscheduled, type Unsent } from "./compose";
import { type Kept, keptFrom, keptText } from "./drafts";
import { conflict } from "./keymap";
import { asked as filingAsked, type Filing, offered, opened, retagged, toggled, told } from "./filing";
import { arrived, type Jumplist, nowhere, step } from "./jumplist";
import type { HistoryView } from "./history";
import { type LinkAct, links } from "./links";
import { ChatImport, idleImport, type ImportCalls, type ImportView } from "./chatimport";
import { Devices, type DevicesCalls, type DevicesView, noDevices } from "./devices";
import { call, subscribe } from "./kith";
import { Login, type LoginCalls, type LoginView } from "./login";
import { type OfferCalls, Offers, type Prompt } from "./offers";
import { noPeople, People, type PeopleCalls, type PeopleView, type Who } from "./people";
import type { Asked } from "./polls";
import { type Bridge, change as changeConfig } from "./config";
import { changeTheme, type ThemeRelay, themeLabel, type Scope, type Slots } from "./theme";
import { announces, readAfter } from "./read";
import { hasUnread, ordered, total } from "./rooms";
import type { When } from "./schedule";
import { emptyFilter, parseSearch } from "./search";
import { change, type Device, folding, type Key, labels, type Read, setFold, withKey } from "./settings";
import { type ListRow, listRows, pinnedThread, type ThreadList } from "./threadrows";
import { OpenThread, type ThreadView } from "./threads";
import {
  answerable,
  heard,
  answering,
  askFailed,
  asked,
  asking,
  canceled,
  dismissed,
  lost,
  noVerify,
  stepped,
  type Verify,
} from "./verify";
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
 * may start before the bridge answers. A reload's page that failed waits as long.
 */
export const deviceAgain = 1000;
/**
 * How long a cursor rests on a room in the list, or on a place on the rail, before its
 * room is shown: a fast run down either shows none on the way.
 */
export const peekAfter = 150;

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
  /** Whose every message it is ("Everything they said"); "" a search typed. */
  about?: string;
}

/** View is everything the window draws. */
export interface View {
  /** The config's profiles, in order, and the one the window runs in: "" asks which. */
  profiles: readonly string[];
  profile: string;
  /** Where kithd is: the local one's socket, or the bridge this device joined (one is ""). */
  socket: string;
  bridge: string;
  link: Link;
  status: StatusResponse | undefined;
  /** The rail's rows (Rail), and the one the window is on (-1: none, no rail yet). */
  rail: readonly RailRow[];
  row: number;
  /** The rooms a silent tag holds (Archived): their unread is drawn, but counts toward no row. */
  silenced: ReadonlySet<string>;
  /** The joined rooms the row lists, in list order (every one before a rail), and the invitations. */
  rooms: readonly Room[];
  /** The room list as drawn: rooms, each with its thread rows under it ([display.threads]). */
  list: readonly ListRow[];
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
  /** The device's [keys]: each action's key sequences by its path, none until read. */
  keys: Readonly<Record<string, readonly string[]>>;
  /** The device's [[keys.jump]]: each chord, and the room or rail row it goes to. */
  jumps: readonly Jump[];
  /** [[keys.tag]]: chords that file the room under the cursor into a tag, or out of it. */
  tagKeys: readonly TagKey[];
  /** [display.media.audio]: the speed a voice note starts at, and the seconds a skip moves. */
  audio: { speed: number; skip: number };
  /** [display.threads]: which of a room's threads the list shows under it; none until read. */
  threads: ThreadList | undefined;
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
  /** The settings page's bridge settings: config.toml's, every device's (config.ts). */
  config: ConfigView;
  /** The open room's remembered sender colors (SenderSlots). */
  slots: Slots;
  /** The open room's people's picture versions (SenderPictures), by their ID. */
  pictures: ReadonlyMap<string, string>;
  /** The settings page's accounts: the networks to log in to, and a login under way. */
  login: LoginView;
  /** The settings page's devices: those paired, a pairing or a join under way (devices.ts). */
  devices: DevicesView;
  /** The settings page's chat import: a chat a phone exported, brought in (chatimport.ts). */
  chatImport: ImportView;
  /** Verifying this session (verify.ts), and the accounts it is asked for from (Accounts). */
  verify: Verify;
  accounts: readonly Account[];
  /** The offers (offers.ts): the one shown, and what the last download came to. */
  offers: { prompt: Prompt | undefined; note: string };
  /** The filing picker open on a room (filing.ts). */
  filing: Filing | undefined;
  /** The note on the bar: what the last act (a filing, a copy, a link, a name) came to. */
  notice: Notice | undefined;
  /** A message's links offered to choose from, for the [keys.timeline] link key pressed. */
  linking: { act: LinkAct; links: readonly string[] } | undefined;
  /** A message's history shown ([keys.timeline] history). */
  history: HistoryView | undefined;
  /** The room whose name is being written (rooms.name), and the name it starts from. */
  naming: { room: string; name: string } | undefined;
  /** The room a key sequence is being written for (/shortcut), and the one it starts from. */
  shortcut: { room: string; keys: string } | undefined;
  /** A rail row's unread rooms asked about marking read (rail.mark_read), and its label. */
  marking: { label: string; rooms: readonly string[] } | undefined;
  /** The People list and a person's card (people.ts). */
  people: PeopleView;
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

/** The bridge's settings on the settings page, each change set at once. */
export interface ConfigView {
  /** The settings as last read or set; undefined while read, or when they could not be. */
  read: ConfigSettingsResponse | undefined;
  /** A change being set: the page takes no other meanwhile. */
  busy: boolean;
  /** What the page says: a change that gave way to another writer's, or a failure. */
  note: string;
}

const noConfig: ConfigView = { read: undefined, busy: false, note: "" };

/** bridgeSettings is the bridge's settings through main: kithd lists them and sets one; the window never holds the file. */
const bridgeSettings: Bridge = {
  read: () => call(backend.configSettings, {}),
  write: (base, path, value) => call(backend.setConfigSetting, { baseRevision: base, path, value }),
};

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

/** loginCalls is a login's calls: kithd's, through main, and the relay's. */
const loginCalls: LoginCalls = {
  networks: () => call(backend.loginNetworks, {}).then((r) => r.networks),
  begin: (network, account) => call(backend.beginLogin, { network, account }).then((r) => r.step),
  answer: (login, values) => call(backend.answerLogin, { login, values }).then((r) => r.step),
  cancel: (login) => call(backend.cancelLogin, { login }).then(() => undefined),
  instead: (login) => call(backend.insteadLogin, { login }).then(() => undefined),
  writeAccount: (record) =>
    call(DeviceService.method.writeAccount, { table: record.table, values: record.values }).then(() => undefined),
  qr: (data) => call(DeviceService.method.qRCode, { data }).then((r) => r.dark),
};

/** importCalls is a chat import's calls: main's (the dialog, the upload, kithd's read), then kithd's. */
const importCalls: ImportCalls = {
  choose: () => window.kith.importChat(),
  senders: (uploadId, zone, roomId) => call(backend.chatImportSenders, { uploadId, zone, roomId }),
  apply: (uploadId, zone, roomId, senders) =>
    call(backend.applyChatImport, { uploadId, zone, roomId, senders }).then((r) => r.added),
};

/** devicesCalls is the devices section's calls: kithd's, through main, and the relay's. */
const devicesCalls: DevicesCalls = {
  devices: () => call(backend.devices, {}),
  remove: (id) => call(backend.removeDevice, { id }).then(() => undefined),
  startPairing: (name, listener) => subscribe(backend.startPairing, { name }, listener),
  confirm: (token, accept) => call(backend.confirmPairing, { token, accept }).then(() => undefined),
  uri: (t) =>
    call(DeviceService.method.pairingURI, {
      token: t.token,
      addresses: t.addresses,
      id: t.id,
      expires: t.expires,
    }).then((r) => r.uri),
  qr: (data) => call(DeviceService.method.qRCode, { data }).then((r) => r.dark),
  join: (uri, name, listener) => subscribe(DeviceService.method.joinBridge, { uri, name }, listener),
};

/** peopleCalls is the phonebook's calls, kithd's through main: the window names a person, never a file. */
const peopleCalls: PeopleCalls = {
  list: (query) => call(backend.people, { query }),
  card: (who) => call(backend.personCard, who),
  set: async (who, name, revision, key, values, list) => {
    const r = await call(backend.setPersonField, { ...who, name, revision, key, values, list });
    if (r.card === undefined) {
      throw new Error("kithd answered no card");
    }
    return r.card;
  },
  keys: () => call(backend.personKeys, {}).then((r) => r.keys),
};

/** offerCalls is the offers' calls: kithd's, through main, and device.toml's through the relay. */
const offerCalls: OfferCalls = {
  settings: () => relay.read().then((r) => r.settings),
  detectLanguages: (owed) => call(backend.detectLanguages, { owed }),
  detectModel: () => call(backend.detectModel, {}),
  install: async (kind, tag) => {
    if (kind === "dictionaries") {
      await call(backend.installDictionary, { tag });
    } else if (kind === "frequencies") {
      await call(backend.installFrequencies, { tag });
    } else {
      await call(backend.installModel, { tag });
    }
  },
  decline: async (kind, tags) => {
    // Added to what the file holds now, written over the file as read (settings.change).
    const shown = await relay.read();
    if (kind === "model") {
      await change(relay, shown, "modelDeclined", true);
    } else {
      const key = kind === "dictionaries" ? "spellDeclined" : "frequenciesDeclined";
      await change(relay, shown, key, [...shown.settings[key], ...tags]);
    }
  },
};

/** errorText is what the settings page, or an editor, says of a failure. */
export function errorText(err: unknown): string {
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
  link: { state: "connecting" },
  status: undefined,
  rail: [],
  row: -1,
  silenced: new Set(),
  rooms: [],
  list: [],
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
  keys: {},
  jumps: [],
  tagKeys: [],
  audio: { speed: 1, skip: 10 },
  threads: undefined,
  composer: undefined,
  slots: new Map(),
  pictures: new Map(),
  verify: noVerify,
  accounts: [],
  filing: undefined,
  naming: undefined,
  shortcut: undefined,
  marking: undefined,
  notice: undefined,
  linking: undefined,
  history: undefined,
  people: noPeople,
} satisfies Partial<View>;

/** Run is one stay in one profile: what it opened, closed together when it ends. */
interface Run {
  profile: string;
  closed: boolean;
  /** The profile's streams. */
  streams: (() => void)[];
  /** Rooms as Rooms last read them (own), and under the names you gave them (joined); when each last had a message. */
  own: readonly Room[];
  joined: readonly Room[];
  at: Readonly<Record<string, bigint>>;
  /** Counts the room reads, so an older answer arriving last is dropped; and whether one has answered. */
  roomReads: number;
  /** Reads the invitations again, as their stream's opening does (keep): kithd names their inviters differently. */
  rereadInvites: () => void;
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
  /** The wait before a failed page nothing else asks for (Timeline.again, OpenThread.again) is asked again. */
  askLater: ReturnType<typeof setTimeout> | undefined;
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
    config: noConfig,
    login: { networks: undefined, run: undefined, note: "" },
    devices: noDevices,
    chatImport: idleImport,
    offers: { prompt: undefined, note: "" },
    problem: undefined,
    ...nothingShown,
  };
  /** The offers of dictionaries, word counts and the completion model: what kithd owes, or was asked. */
  private readonly offers = new Offers(offerCalls, () => {
    this.set({ offers: { prompt: this.offers.prompt, note: this.offers.note } });
  });
  private readonly listeners = new Set<() => void>();
  private run: Run | undefined;
  /** This window, to kithd's windows (main names it so, whatever this says). */
  private readonly client = crypto.randomUUID();
  /**
   * The DeviceService calls, one after another: main moves to the profile each answer
   * leaves it in (runIn), and so does the window, so both read them in one order.
   */
  private device: Promise<void> = Promise.resolve();
  /** The device read again after one failed, while it waits; none once stopped. */
  private deviceLater: ReturnType<typeof setTimeout> | undefined;
  /** The notice's going by itself, after its level's [gui.notices] seconds. */
  private noticeLater: ReturnType<typeof setTimeout> | undefined;
  private stopped = false;
  private told = 0;
  /** The open room is watched: shown at its newest, the window focused. */
  private watching = false;
  /** The focus is in the open room (its messages, thread or composer): what it shows is read. */
  private entered = false;
  /**
   * The row the room list's cursor is on: a room's, or one of its threads' (root). A
   * thread's row reads that thread only: resting on it, or being in the thread it opened,
   * reads nothing of its room.
   */
  private cursor = { room: "", root: "" };
  /** The room whose "+N more threads" row was entered: all its threads listed until it is left, or leaves the list. */
  private expanded = "";
  /** The folds being written, one after another, and how many are still to answer. */
  private folds: Promise<void> = Promise.resolve();
  private foldsPending = 0;
  /** The room the list's cursor rested on for its read_delay, read as if entered; and the wait for it. */
  private rested = "";
  private resting: ReturnType<typeof setTimeout> | undefined;
  /** The wait before the place the rail moved to shows its room. */
  private railing: ReturnType<typeof setTimeout> | undefined;
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
  /**
   * Words a profile left (or the window before a reload) did not get stored, by room, and
   * the rooms' names: back in its composer once it is open again, said until then.
   */
  private readonly kept = new Map<string, Kept>();
  private readonly keptNames = new Map<string, string>();
  /** The open thread is watched: its pane shown, the window focused. */
  private watchingThread = false;
  /** Counts the problems said; and the last, when a page's: taken down once its reader reads. */
  private said = 0;
  private pageProblem: { reader: Timeline | OpenThread; said: number } | undefined;
  /** Counts the settings page's reads and writes: only the last one's answer is shown. */
  private settingsReads = 0;
  /** Counts the theme's reads and writes, likewise; and the keys' reads. */
  private themeReads = 0;
  private keyReads = 0;
  /** Counts the bridge settings' reads and changes, likewise; and the page's openings and closings. */
  private configReads = 0;
  private configPage = 0;
  /** What the window last told kithd it has open, and when. */
  private presented = { room: "", at: 0 };
  /** Counts the filing pickers opened: only the last one's places are shown. */
  private filings = 0;
  /** A login the window leads; one that logged in has the rail and rooms read again. */
  private readonly login = new Login(
    loginCalls,
    (login) => {
      this.set({ login });
    },
    () => {
      this.reread();
    },
  );

  /** The devices paired, and a pairing or a join the window leads. */
  private readonly devices = new Devices(devicesCalls, (devices) => {
    this.set({ devices });
  });

  /** A chat export being imported, from the settings page. */
  private readonly chatImport = new ChatImport(importCalls, (chatImport) => {
    this.set({ chatImport });
  });

  /** The People list and a person's card, in the profile the window runs in. */
  private readonly people = new People(peopleCalls, (people) => {
    this.set({ people });
  });

  /** start reads the device, which runs the window in a profile or has it ask. */
  start(): void {
    this.stopped = false; // StrictMode starts a window again after stopping it
    this.readDevice();
    // Closing, the window writes its drafts first (main waits a little for it); reloading,
    // it has no time to, and keeps them for the window loaded again.
    window.kith.closing(() => this.run?.composer.drafts.flush() ?? Promise.resolve());
    for (const [room, kept] of keptFrom(window.kith.kept())) {
      this.kept.set(room, kept);
    }
    window.kith.reloading(() => keptText(new Map([...this.kept, ...(this.run?.composer.drafts.unstored() ?? [])])));
  }

  /** stop closes every stream: the window is going. */
  stop(): void {
    this.stopped = true;
    this.login.stop();
    this.devices.stop();
    clearTimeout(this.deviceLater);
    clearTimeout(this.railing);
    clearTimeout(this.noticeLater);
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
        // The drafts are written first, in the profile they were typed in: once main has
        // moved, kithd refuses a room the new profile does not hold.
        await this.run?.composer.drafts.flush();
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
    this.configPage++;
    this.set({
      settingsPage: { read: undefined, busy: false, note: "" },
      themeChange: { busy: false, note: "" },
      config: noConfig,
    });
    this.readTheme(); // device.toml may have been changed since, by the TUI or by hand
    this.readConfig();
    this.login.read();
    this.devices.read();
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
    this.configReads++;
    this.configPage++;
    this.devices.closePairing();
    this.devices.dismissJoin();
    this.chatImport.cancel();
    this.set({ settingsPage: undefined, config: noConfig });
  }

  /** chooseExport asks for a chat export to import, which kithd reads. */
  chooseExport(): void {
    this.chatImport.choose();
  }

  /** importInto matches the export's names to room's members; name is what the page calls it. */
  importInto(room: string, name: string): void {
    this.chatImport.room(room, name);
  }

  /** importAs takes the export's name as the member id ("" as written). */
  importAs(name: string, id: string): void {
    this.chatImport.whoIs(name, id);
  }

  /** importBack goes back to choosing the chat. */
  importBack(): void {
    this.chatImport.back();
  }

  /** applyImport imports the export as chosen. */
  applyImport(): void {
    this.chatImport.apply();
  }

  /** cancelImport drops the import under way. */
  cancelImport(): void {
    this.chatImport.cancel();
  }

  /** pairDevice opens a pairing for a new device, listed as name ("": the name it gives). */
  pairDevice(name: string): void {
    this.devices.pair(name);
  }

  /** confirmPairing asks main to pair the device that answered: main asks the person first. */
  confirmPairing(): void {
    this.devices.confirm();
  }

  /** refusePairing ends the pairing: nothing is paired. */
  refusePairing(): void {
    this.devices.refuse();
  }

  /** closePairing ends the pairing shown; one not confirmed pairs nothing. */
  closePairing(): void {
    this.devices.closePairing();
  }

  /** unpairDevice asks main to unpair device: main asks the person first. */
  unpairDevice(device: PairedDevice): void {
    this.devices.unpair(device);
  }

  /** joinBridge asks main to join the bridge uri names, as kith join does: main asks the person first. */
  joinBridge(uri: string, name: string): void {
    this.devices.join(uri, name);
  }

  /** dismissJoin drops the join shown; one under way is cut, unconfirmed. */
  dismissJoin(): void {
    this.devices.dismissJoin();
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
   * changeConfig sets one of the bridge's settings at once (undefined clears it), onto the
   * file as another writer may have left it meanwhile (config.change). The page shows the
   * setting as kithd then holds it. The config is the bridge's, so a change asked in one
   * profile stands whichever the window runs in by the time it is answered; one answered
   * after the page closed is dropped.
   */
  async changeConfig(path: string, value: ConfigValue | undefined): Promise<void> {
    const shown = this.view.config.read;
    if (shown === undefined || this.view.config.busy) {
      return;
    }
    const mine = ++this.configReads; // a read under way shows the file from before this change: dropped
    const page = this.configPage;
    const label = shown.settings.find((s) => s.path === path)?.label ?? path;
    // Shown as asked while it is set, so a control does not spring back meanwhile.
    const asked = { ...shown, settings: shown.settings.map((s) => (s.path === path ? { ...s, value } : s)) };
    this.set({ config: { read: asked, busy: true, note: "" } });
    let read = shown;
    let note = "";
    try {
      const got = await changeConfig(bridgeSettings, shown, path, value);
      read = got.read;
      if (got.kept) {
        note = `${label} was changed elsewhere meanwhile: the page shows it as it is now.`;
      }
    } catch (err) {
      note = `${label} was not saved: ${errorText(err)}`;
    }
    if (page !== this.configPage) {
      return;
    }
    // A read started since (a change told on ConfigChanged, this one's or another
    // writer's) shows the file as it is, or will once answered; else this answer does.
    this.set({ config: { read: mine === this.configReads ? read : this.view.config.read, busy: false, note } });
  }

  /**
   * readConfig reads the bridge's settings for the settings page, keeping what it says
   * meanwhile. Only the last read or change started is shown: a change's own write is
   * told on ConfigChanged, so a read from before it is followed by one from after.
   */
  private readConfig(): void {
    const mine = ++this.configReads;
    bridgeSettings.read().then(
      (read) => {
        if (mine === this.configReads) {
          this.set({ config: { ...this.view.config, read } });
        }
      },
      (err: unknown) => {
        if (mine === this.configReads) {
          this.set({
            config: { ...this.view.config, note: `The bridge's settings could not be read: ${errorText(err)}` },
          });
        }
      },
    );
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

  /** beginLogin has kithd log a network's account in ("" sets a new one up). */
  beginLogin(network: LoginNetwork, account: string): void {
    this.login.begin(network, account);
  }

  /** answerLogin answers the login's step: the values go to kithd, and are not kept. */
  answerLogin(values: Readonly<Record<string, string>>): void {
    this.login.submit(values);
  }

  /** cancelLogin abandons the login under way. */
  cancelLogin(): void {
    this.login.cancel();
  }

  /** takeInstead takes the other way the login's QR step offers. */
  takeInstead(): void {
    this.login.instead();
  }

  /**
   * signOut signs out the account whose own space space is, as the TUI's rail does:
   * forget drops its rooms and their history from the cache too.
   */
  async signOut(space: string, label: string, forget: boolean): Promise<void> {
    this.login.say(`Signing out of ${label}…`);
    try {
      await call(backend.signOut, { spaceId: space, forget });
      this.login.say(`Signed out of ${label}.`);
      this.reread();
    } catch (err) {
      this.login.say(`Could not sign out of ${label}: ${errorText(err)}`);
    }
  }

  /** identities are the config's people ([[display.identity]]), which an account may join. */
  async identities(): Promise<Identity[]> {
    return (await call(backend.identities, {})).identities;
  }

  /**
   * setIdentity makes the account id one of the person alias, with their color and
   * picture (setup.FacesOf's: "" each their own, one of their IDs, or a saved picture);
   * the theme and the open room's pictures are read again, as they follow it.
   */
  async setIdentity(id: string, alias: string, color: string, picture: string): Promise<void> {
    await call(backend.setIdentity, { id, alias, color, picture });
    this.readTheme();
    const run = this.run;
    if (run !== undefined && this.live(run) && this.view.open !== "") {
      this.readPictures(run, this.view.open);
    }
  }

  /** pickPicture asks for an image in the open dialog and keeps it as a picture: its name, or undefined for none picked. */
  async pickPicture(): Promise<string | undefined> {
    const [offer] = await window.kith.pick();
    return offer === undefined ? undefined : window.kith.savePicture(offer.token);
  }

  /** reread reads the rail and the rooms again: an account came or went. */
  private reread(): void {
    const run = this.run;
    if (run !== undefined && this.live(run)) {
      this.readRail(run);
      this.readRooms(run);
    }
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

  /** readKeys reads the device's keys again; ones that cannot be read leave the last ones bound. */
  private readKeys(): void {
    const mine = ++this.keyReads;
    relay.read().then(
      ({ settings }) => {
        if (mine === this.keyReads && !this.stopped) {
          this.set({
            keys: Object.fromEntries(Object.entries(settings.keys).map(([path, k]) => [path, k.sequences])),
            jumps: settings.jumps,
            tagKeys: settings.tagKeys,
            audio: { speed: settings.audioSpeed, skip: Number(settings.audioSkip) },
            threads: settings,
          });
        }
      },
      () => undefined,
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

  /** readPictures reads the room's people's pictures, read again as the rooms change: a sweep found new ones. */
  private readPictures(run: Run, room: string): void {
    call(backend.senderPictures, { roomId: room }).then(
      (r) => {
        if (this.live(run) && this.view.open === room) {
          this.set({ pictures: new Map(Object.entries(r.pictures)) });
        }
      },
      () => undefined, // drawn in their initials
    );
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

  /** peek shows room as the list's cursor rests on it, without going there: back and forward pass it by. */
  peek(room: string): void {
    const run = this.run;
    if (run?.listed === true && this.holds(run, room)) {
      this.show(run, room);
    }
  }

  /**
   * inRoom says where in the open room the focus is: its messages or composer ("room"),
   * its thread, or neither (""). Only then is what it shows read, or once the list's
   * cursor rests on it for read_delay. A thread opened from its row in the list reads
   * that thread, not the room beside it.
   */
  inRoom(where: "room" | "thread" | ""): void {
    const fromRow = this.cursor.root !== "" && this.cursor.root === this.run?.thread?.root;
    this.entered = where === "room" || (where === "thread" && !fromRow);
    if (this.entered && this.run !== undefined) {
      this.markRead(this.run);
    }
  }

  /** cursorOn says which row the room list's cursor is on: room's, or its thread root's ("" the room's). */
  cursorOn(room: string, root: string): void {
    if (this.cursor.room === room && this.cursor.root === root) {
      return;
    }
    this.cursor = { room, root };
    if (root !== "") {
      clearTimeout(this.resting); // a thread's row reads nothing of its room
      this.resting = undefined;
      this.rested = "";
    }
    this.set({});
    if (root === "" && this.run !== undefined) {
      this.markRead(this.run); // back on the room's row: it rests there again
    }
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
    clearTimeout(this.resting);
    this.resting = undefined;
    this.rested = "";
    run.timeline = new Timeline(room, this.view.unread.get(room)?.readEvent ?? "");
    run.settings = undefined;
    const kept = this.kept.get(room);
    const said = this.unsaved();
    if (kept !== undefined) {
      this.kept.delete(room);
      this.keptNames.delete(room);
      run.composer.drafts.keep(room, kept);
    }
    run.composer.opened(room);
    this.set({
      open: room,
      unreadLine: true,
      slots: new Map(),
      pictures: new Map(),
      ...(said !== undefined && this.view.problem === said ? { problem: this.unsaved() } : {}),
    });
    this.readSettings(run, run.timeline);
    this.readSlots(run, room);
    this.readPictures(run, room);
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

  /** moved is the caret moved in the open composer, nothing typed. */
  moved(caret: number): void {
    this.run?.composer.moved(caret);
  }

  /** complete takes what completion offers (all: Tab, else right); undefined: nothing was. */
  complete(all: boolean): number | undefined {
    return this.run?.composer.complete(this.view.open, all);
  }

  /** chooseCompletion takes the n-th completion on offer (alt+1…); undefined: there is none. */
  chooseCompletion(n: number): number | undefined {
    return this.run?.composer.choose(this.view.open, n);
  }

  /** openWalk opens the correction walk, at the at-th mark. */
  openWalk(at?: number): void {
    this.run?.composer.openWalk(at);
  }

  /** walkTake takes the walk's n-th suggestion; it answers the caret, if the draft changed. */
  walkTake(n: number): number | undefined {
    return this.run?.composer.walkTake(n);
  }

  /** walkLearn adds the walk's word to the dictionary (forever), or ignores it this session. */
  walkLearn(forever: boolean): void {
    this.run?.composer.walkLearn(forever);
  }

  walkSkip(): void {
    this.run?.composer.walkSkip();
  }

  walkIgnoreAll(): void {
    this.run?.composer.walkIgnoreAll();
  }

  /** walkSkipAll leaves every word left and finishes: a send that opened it goes. */
  walkSkipAll(): Promise<void> {
    return this.inRun((run) => run.composer.walkEnd());
  }

  walkStop(): void {
    this.run?.composer.walkStop();
  }

  /** acceptOffer installs the offer's rows ticked and turns down the rest. */
  acceptOffer(ticked: readonly string[]): void {
    void this.offers.accept(ticked);
  }

  dismissOffer(): void {
    this.offers.dismiss();
  }

  /** findDictionaries offers the dictionaries and word counts for what you write, read now. */
  findDictionaries(): void {
    void this.offers.find();
  }

  forgetOfferNote(): void {
    this.offers.forget();
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

  /** later queues the open composer's words to send at a time given as kind says. */
  later(kind: When, text: string): Promise<void> {
    const room = this.view.open;
    return this.inRun((run) => run.composer.later(room, kind, text));
  }

  scheduleAgain(u: Unscheduled): Promise<void> {
    return this.inRun((run) => run.composer.scheduleAgain(u));
  }

  dismissUnscheduled(u: Unscheduled): void {
    this.run?.composer.dismissUnscheduled(u);
  }

  /** showQueue shows the send queue: the open room's, or every room's. */
  showQueue(everyRoom: boolean): void {
    this.run?.composer.showQueue(everyRoom ? "" : this.view.open);
  }

  closeQueue(): void {
    this.run?.composer.closeQueue();
  }

  cancelQueued(id: string): Promise<void> {
    return this.inRun((run) => run.composer.cancelQueued(id));
  }

  retryQueued(id: string): Promise<void> {
    return this.inRun((run) => run.composer.retryQueued(id));
  }

  /** wantCommands reads the commands folder's commands for the open room's `/` list. */
  wantCommands(): void {
    this.run?.composer.wantCommands(this.view.open);
  }

  chooseLink(i: number): Promise<void> {
    return this.inRun((run) => run.composer.chooseLink(i));
  }

  closeChoosing(): void {
    this.run?.composer.closeChoosing();
  }

  closeReading(): void {
    this.run?.composer.closeReading();
  }

  /** save saves m's attachment where the person picks. */
  save(m: Message): Promise<void> {
    return this.inRun(async () => {
      await window.kith.save(m.roomId, m.id, m.media?.name ?? "");
    });
  }

  /** copyText puts m's text on the clipboard: an attachment's caption, or its name where it has none. */
  copyText(m: Message): void {
    if (m.redacted) {
      this.set(this.noticed("that message was deleted — nothing to copy", "warning"));
    } else if (m.body.trim() === "") {
      this.set(this.noticed("nothing to copy", "warning"));
    } else {
      this.copyOut(m.body, "message");
    }
  }

  /** takeLink copies or opens one of m's web links: its only one, or the one picked (pickLink). */
  takeLink(m: Message, act: LinkAct): void {
    const found = links(m);
    const only = found.length === 1 ? found[0] : undefined;
    if (only !== undefined) {
      this.useLink(act, only);
    } else {
      this.set(
        found.length === 0
          ? this.noticed("no link in that message", "warning")
          : { linking: { act, links: found }, notice: undefined },
      );
    }
  }

  /** pickLink takes the i-th link offered (from 0) for the key that offered them. */
  pickLink(i: number): void {
    const l = this.view.linking;
    const link = l?.links[i];
    this.set({ linking: undefined });
    if (l !== undefined && link !== undefined) {
      this.useLink(l.act, link);
    }
  }

  closeLinking(): void {
    this.set({ linking: undefined });
  }

  /** openHistory shows m's history: every version it had, and its deletion (MessageHistory). */
  openHistory(m: Message): void {
    const run = this.run;
    if (run === undefined) {
      return;
    }
    const shown = (change: Partial<HistoryView>) => {
      if (this.live(run) && this.view.history?.message === m) {
        this.set({ history: { ...this.view.history, ...change } });
      }
    };
    this.set({ history: { message: m, revisions: undefined, deletion: undefined, error: "" }, notice: undefined });
    call(backend.messageHistory, { roomId: m.roomId, eventId: m.id }).then(
      (r) => {
        shown({ revisions: r.revisions, deletion: r.deletion });
      },
      (err: unknown) => {
        shown({ error: errorText(err) });
      },
    );
  }

  closeHistory(): void {
    this.set({ history: undefined });
  }

  /**
   * useLink copies link, or hands it to kithd (Follow), whose link rules pick the window
   * that follows it, this one or another, TUI or desktop alike; this one follows it on its
   * link stream, as a link clicked elsewhere. A web link kithd does not follow is the
   * browser's.
   */
  private useLink(act: LinkAct, link: string): void {
    if (act === "copy") {
      this.copyOut(link, "link");
      return;
    }
    call(backend.follow, { uri: link }).then(
      (r) => {
        if (r.web && !this.stopped) {
          this.browse(link, act === "open");
        }
      },
      (err: unknown) => {
        if (!this.stopped) {
          this.set(this.noticed(`the link was not followed: ${errorText(err)}`, "error"));
        }
      },
    );
  }

  /** browse opens a web link in the browser, the window brought forward again after where stay. */
  private browse(link: string, stay: boolean): void {
    const host = link.replace(/^https?:\/\//i, "").split("/")[0] ?? link;
    this.set(this.noticed(`opening ${host}…`, "message"));
    window.kith.open(link, stay).then(
      (went) => {
        if (!went && !this.stopped) {
          this.set(this.noticed("refusing to open that — only http and https links", "warning"));
        }
      },
      (err: unknown) => {
        if (!this.stopped) {
          this.set(this.noticed(`${host} did not open: ${errorText(err)}`, "error"));
        }
      },
    );
  }

  /** copyOut puts text on the clipboard and says so, what it is and its start. */
  private copyOut(text: string, what: string): void {
    const flat = text.split(/\s+/).filter(Boolean).join(" ");
    const shown = flat.length > 48 ? `${flat.slice(0, 47)}…` : flat;
    window.kith.copy(text).then(
      () => {
        if (!this.stopped) {
          this.set(this.noticed(`copied ${what}: ${shown}`, "message"));
        }
      },
      (err: unknown) => {
        if (!this.stopped) {
          this.set(this.noticed(`the ${what} was not copied: ${errorText(err)}`, "error"));
        }
      },
    );
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
      this.set({ profiles: d.profiles, socket: d.socket, bridge: d.bridge, problem: this.unsaved() });
      this.readTheme(); // read as the profiles are: at the start, and after each config change
      this.readKeys();
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
   * enter runs the window in profile: what the last one showed goes, and the profile's
   * streams open. "" shows the picker. The profile it already runs in is left as it is.
   */
  private enter(profile: string): void {
    if (profile === this.view.profile) {
      return;
    }
    // Main ends the last profile's link stream: links it handed meanwhile still arrive.
    this.leave(true);
    this.set({ ...nothingShown, profile });
    if (profile !== "") {
      const run = this.begin(profile);
      this.follow(run);
      run.composer.start();
    }
  }

  private begin(profile: string): Run {
    const run: Run = {
      profile,
      closed: false,
      streams: [],
      own: [],
      joined: [],
      at: {},
      roomReads: 0,
      rereadInvites: () => undefined,
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
      askLater: undefined,
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
        messages: (room) => (run.timeline?.room === room ? run.timeline.view().rows : []),
        spaceRooms: (room) => spaceRooms(run.rail?.rows ?? [], room),
        search: (terms) => {
          this.search(terms, "room");
        },
        pick: () => void this.pick(),
        go: (room) => {
          if (!this.holds(run, room)) {
            return false;
          }
          this.open(room);
          return true;
        },
        shortcut: (room, keys) => {
          if (keys === "") {
            const keys = this.view.jumps.find((j) => j.room === room)?.sequence ?? "";
            this.set({ shortcut: { room, keys }, notice: undefined });
          } else {
            void this.bindShortcut(room, keys);
          }
        },
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
    this.people.reset();
    run.closed = true;
    run.composer.stop();
    this.keepUnstored(run);
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

  /**
   * keepUnstored holds the words of run's drafts kithd is not known to have stored, and
   * says so: they are put back in the composer when their room is open again (show).
   */
  private keepUnstored(run: Run): void {
    const unstored = run.composer.drafts.unstored();
    for (const [room, kept] of unstored) {
      this.kept.set(room, kept);
      this.keptNames.set(room, run.joined.find((r) => r.id === room)?.name ?? room);
    }
    if (unstored.size > 0) {
      this.fail(this.unsaved());
    }
  }

  /**
   * unsaved says which drafts the window keeps unsaved, if any: what a read that takes
   * the problems said down (Device, Status) says instead, until they are written.
   */
  private unsaved(): string | undefined {
    if (this.kept.size === 0) {
      return undefined;
    }
    const name = (room: string) => this.keptNames.get(room) ?? this.run?.joined.find((r) => r.id === room)?.name;
    const rooms = [...this.kept.keys()].map((room) => name(room) ?? room);
    return `The draft in ${rooms.join(", ")} is not saved: it is kept here until the room is open again`;
  }

  private live(run: Run): boolean {
    return !run.closed && run === this.run;
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
            this.readAccounts(run);
            this.readRooms(run);
            this.readRail(run);
            break;
          case "frames":
            if (e.frames.some((f) => f.change === Change.ROOMS)) {
              this.readRooms(run);
              if (this.view.open !== "") {
                this.readPictures(run, this.view.open);
              }
            }
            if (e.frames.some((f) => f.change === Change.RAIL)) {
              this.readRail(run);
            }
            if (e.frames.some((f) => f.change === Change.ACCOUNTS)) {
              this.readStatus(run);
              this.readAccounts(run);
            }
            if (e.frames.some((f) => f.change === Change.LANGUAGES)) {
              void this.offers.owed();
            }
            if (e.frames.some((f) => f.change === Change.DRAFTS)) {
              // Another window, or kith-mcp, wrote a draft: each changed one is taken
              // where nothing typed here is unsaved (Drafts.load).
              void run.composer.drafts.load();
            }
            if (e.frames.some((f) => f.change === Change.NAMES)) {
              // kithd names people differently: what shows them is read again.
              run.timeline?.reload();
              run.thread?.reload();
              run.composer.forgetMembers();
              run.composer.rereadVoters();
              run.rereadInvites();
              this.pumpThread(run);
              this.pump(run);
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
          if (this.view.settingsPage !== undefined) {
            this.readConfig(); // another writer's change shows on the page
          }
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
      // Verifications: none is read whole (kithd has no read of them), and one the window
      // showed ends with the link: a step sent meanwhile is not sent again.
      subscribe(backend.verifications, {}, (e) => {
        if (!this.live(run)) {
          return;
        }
        if (e.kind === "frames") {
          let verify = this.view.verify;
          for (const f of e.frames) {
            if (f.verification !== undefined) {
              verify = stepped(verify, f.verification);
            }
          }
          this.set({ verify });
        } else if (e.kind === "detached") {
          this.set({ verify: lost(this.view.verify) });
        } else if (e.kind === "refused") {
          this.fail(e.error);
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
    run.rereadInvites = this.keep(
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
   * carries a room's whole count, or the whole set). What it returns reads it again so.
   */
  private keep<I extends DescMessage, O extends DescMessage, S>(
    run: Run,
    method: DescMethodServerStreaming<I, O>,
    input: MessageInitShape<I>,
    read: () => Promise<S>,
    replace: (state: S) => void,
    apply: (frame: MessageShape<O>) => void,
  ): () => void {
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
    const reread = () => {
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
    };
    run.streams.push(
      subscribe(method, input, (e) => {
        if (!this.live(run)) {
          return;
        }
        switch (e.kind) {
          case "attached":
            reread();
            break;
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
    return reread;
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
          this.set(this.pageRead(t));
        }
      },
      (err: unknown) => {
        if (this.live(run) && run.timeline === t) {
          if (t.failed(ticket)) {
            this.askLater(run);
          }
          if (ticket.ask.kind !== "network") {
            this.failPage(t, err); // a network's page hands over to the cache's, unsaid
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
          this.set(this.pageRead(t));
        }
      },
      (err: unknown) => {
        if (this.live(run) && run.thread === t) {
          if (t.failed(ticket)) {
            this.askLater(run);
          }
          if (ask.kind === "page") {
            this.failPage(t, err); // a root not cached is shown without it, unsaid
          }
          this.set({});
        }
      },
    );
  }

  /**
   * askLater asks again, after a wait, for the failed pages nothing else asks for: a
   * reload's or a room's first (the timeline's), a reload's (the thread's). One wait at a
   * time; it acts on the room and thread open when it is over, and asks nothing for a
   * profile the window has left (pump and pumpThread).
   */
  private askLater(run: Run): void {
    run.askLater ??= setTimeout(() => {
      run.askLater = undefined;
      run.timeline?.again();
      run.thread?.again();
      this.pump(run);
      this.pumpThread(run);
      this.set({});
    }, deviceAgain);
  }

  /** failPage says a page's failure, to be taken down once reader reads (pageRead). */
  private failPage(reader: Timeline | OpenThread, err: unknown): void {
    this.fail(err);
    this.pageProblem = { reader, said: this.said };
  }

  /** pageRead takes down the problem reader's failed page said, while it is the last said. */
  private pageRead(reader: Timeline | OpenThread): Partial<View> {
    const p = this.pageProblem;
    if (p?.reader !== reader) {
      return {};
    }
    this.pageProblem = undefined;
    return p.said === this.said ? { problem: undefined } : {};
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

  /** rest reads room once the list's cursor has rested on it, shown, for after seconds; negative never. */
  private rest(run: Run, room: string, after: number): void {
    if (after < 0 || this.resting !== undefined) {
      return;
    }
    this.resting = setTimeout(() => {
      this.resting = undefined;
      if (this.live(run) && run.timeline?.room === room) {
        this.rested = room;
        this.markRead(run);
      }
    }, after * 1000);
  }

  /**
   * markRead sends a receipt for the open room's newest message while it is watched and
   * entered (or rested on in the list), once its settings are read: announced where they
   * say so, else private, which still clears its badge everywhere. Each message is marked
   * once; a failed receipt is sent again with the next.
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
    if (!this.entered && this.rested !== t.room) {
      if (this.cursor.root === "" || this.cursor.room !== t.room) {
        this.rest(run, t.room, readAfter(settings, t.room, last.sender));
      }
      return;
    }
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
          run.own = rooms.rooms;
          run.joined = named(run.own, run.rail);
          run.listed = true;
          run.at = last.at;
          if (rooms.rooms.length > 0) {
            void this.offers.start(); // once: the cache has words to count
          }
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
        run.joined = named(run.own, rail);
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
    // The timeline shows what the room list does once the rail rests: the open room where
    // the place lists it, else its first, else none.
    clearTimeout(this.railing);
    this.railing = setTimeout(() => {
      this.railing = undefined;
      if (!this.live(run) || this.view.rooms.some((r) => r.id === this.view.open)) {
        return;
      }
      const first = this.view.rooms[0];
      if (first === undefined) {
        this.hide(run);
      } else {
        this.peek(first.id);
      }
    }, peekAfter);
  }

  /** hide shows no room: the place the rail moved to lists none. */
  private hide(run: Run): void {
    if (this.view.open === "") {
      return;
    }
    this.closeThread();
    clearTimeout(this.resting);
    this.resting = undefined;
    this.rested = "";
    run.timeline = undefined;
    this.set({ open: "" });
    this.present(run);
  }

  /**
   * openFiling opens the filing picker on room, one of the profile's: the places kithd
   * offers it (FilingPlaces, with this device's [display] filing_spaces), ticked where they
   * hold it. An answer for a picker since closed, or for another profile, is dropped.
   */
  openFiling(room: string): void {
    const run = this.run;
    if (run === undefined || !this.holds(run, room) || this.view.filing?.busy === true) {
      return;
    }
    const mine = ++this.filings;
    const name = run.joined.find((r) => r.id === room)?.name ?? "";
    this.set({ filing: opened(room, name || room), notice: undefined });
    const shown = () => this.live(run) && this.filings === mine && this.view.filing?.room === room;
    relay
      .read()
      .then(({ settings }) => call(backend.filingPlaces, { roomId: room, filingSpaces: settings.filingSpaces }))
      .then(
        (r) => {
          const f = this.view.filing;
          if (shown() && f !== undefined) {
            this.set({ filing: offered(f, r.places, r.unmatched) });
          }
        },
        (err: unknown) => {
          const f = this.view.filing;
          if (shown() && f !== undefined) {
            this.set({ filing: { ...f, note: errorText(err) } });
          }
        },
      );
  }

  /**
   * tagRoom is a [[keys.tag]] chord on room, as the TUI's room list takes it: room goes
   * into tag, or out of it if it is in. kithd says where the room is (FilingPlaces) and
   * files the one change (FileRoom); the line under the rooms says what came of it.
   */
  tagRoom(room: string, tag: string): void {
    const run = this.run;
    if (run === undefined || !this.holds(run, room)) {
      return;
    }
    const name = run.joined.find((r) => r.id === room)?.name || room;
    relay
      .read()
      .then(({ settings }) => call(backend.filingPlaces, { roomId: room, filingSpaces: settings.filingSpaces }))
      .then((r) => {
        const f = retagged(offered(opened(room, name), r.places, false), tag);
        if (f === undefined) {
          return this.noticed(`no tag is called “${tag}”: see [keys.tag]`, "warning");
        }
        const ask = filingAsked(f);
        return call(backend.fileRoom, { roomId: room, tags: ask.tags, spaces: [] }).then((resp) =>
          this.noticed(told(f, ask, resp), toldLevel(resp)),
        );
      })
      .then(
        (said) => {
          if (this.live(run)) {
            this.set(said);
          }
        },
        (err: unknown) => {
          if (this.live(run)) {
            this.set(this.noticed(`${name} was not filed: ${errorText(err)}`, "error"));
          }
        },
      );
  }

  /** openNaming asks for a room's name, starting from the one it shows. */
  openNaming(room: string): void {
    const run = this.run;
    if (run !== undefined && this.holds(run, room)) {
      this.set({ naming: { room, name: run.joined.find((r) => r.id === room)?.name ?? "" }, notice: undefined });
    }
  }

  closeNaming(): void {
    this.set({ naming: undefined });
  }

  /**
   * nameRoom gives the room being named the name typed, as the TUI's rooms.name does: a
   * name of your own in [[display.name]], which kithd writes and the rail then serves;
   * an empty one takes it away. The line under the rooms says what came of it.
   */
  nameRoom(name: string): void {
    const run = this.run;
    const asked = this.view.naming;
    if (run === undefined || asked === undefined) {
      return;
    }
    this.set({ naming: undefined });
    const typed = name.trim();
    const own = run.own.find((r) => r.id === asked.room)?.name || asked.room;
    call(backend.setRoomName, { roomId: asked.room, name: typed }).then(
      () => {
        if (this.live(run)) {
          this.set(
            this.noticed(typed === "" ? `${own} has its own name again` : `${own} is ${typed} for you`, "message"),
          );
        }
      },
      (err: unknown) => {
        if (this.live(run)) {
          this.set(this.noticed(`${asked.name} was not named: ${errorText(err)}`, "error"));
        }
      },
    );
  }

  closeShortcut(): void {
    this.set({ shortcut: undefined });
  }

  /**
   * bindShortcut makes keys the sequence that goes to room, as the TUI's /shortcut does:
   * a [[keys.jump]] in device.toml, the room's earlier one replaced; empty keys unbind
   * it. A sequence already bound, or one that starts or is started by a bound one, is
   * refused. The line under the rooms says what came of it.
   */
  async bindShortcut(room: string, keys: string): Promise<void> {
    const run = this.run;
    if (run === undefined) {
      return;
    }
    this.set({ shortcut: undefined });
    const seq = keys.trim().split(/\s+/u).filter(Boolean).join(" ");
    const name = run.joined.find((r) => r.id === room)?.name || room;
    const bound: Record<string, readonly string[]> = { ...this.view.keys };
    for (const j of this.view.jumps) {
      if (j.room !== room) {
        bound[`a jump to ${j.place || (run.joined.find((r) => r.id === j.room)?.name ?? j.room)}`] = [j.sequence];
      }
    }
    for (const t of this.view.tagKeys) {
      bound[`putting rooms in ${t.tag}`] = [t.sequence];
    }
    const why = seq === "" ? "" : conflict(seq, bound);
    if (why !== "") {
      this.set(this.noticed(`${seq}: ${why}`, "warning"));
      return;
    }
    let note: Notice;
    try {
      const shown = await relay.read();
      const jumps = shown.settings.jumps.filter((j) => j.room !== room);
      if (seq !== "") {
        jumps.push(create(JumpSchema, { sequence: seq, room }));
      }
      const got = await change(relay, shown, "jumps", jumps);
      if (this.live(run)) {
        this.set({ jumps: got.settings.jumps });
      }
      note = got.kept
        ? { text: `${labels.jumps} were changed elsewhere meanwhile: ${name} was not bound`, level: "warning" }
        : { text: seq === "" ? `unbound ${name}` : `${seq} now goes to ${name}`, level: "message" };
    } catch (err) {
      note = { text: `${name} was not bound: ${errorText(err)}`, level: "error" };
    }
    if (this.live(run)) {
      this.set(this.noticed(note.text, note.level));
    }
  }

  /** tickFiling ticks a place of the open filing picker, or clears it. */
  tickFiling(key: string): void {
    const f = this.view.filing;
    if (f !== undefined) {
      this.set({ filing: toggled(f, key) });
    }
  }

  /** closeFiling puts the filing picker away; a filing already with kithd still says what it came to. */
  closeFiling(): void {
    this.filings++;
    this.set({ filing: undefined });
  }

  /** markRoomRead marks a room read without opening it (rooms.mark_read), as the TUI's does. */
  markRoomRead(room: string): void {
    const run = this.run;
    if (run !== undefined && this.holds(run, room)) {
      this.markRooms(run, [room], run.joined.find((r) => r.id === room)?.name || room);
    }
  }

  /**
   * foldThreads hides room's thread rows in the list, or shows them again (rooms.fold_threads),
   * remembered in the device file ([display.threads] folded) as the TUI's are. Drawn at once;
   * written in turn, each on the file as read then (setFold).
   */
  foldThreads(room: string): void {
    const threads = this.view.threads;
    if (threads === undefined) {
      return;
    }
    const fold = !threads.threadsFolded.includes(room);
    this.set({ threads: { ...threads, threadsFolded: folding(threads.threadsFolded, room, fold) } });
    this.foldsPending++;
    this.folds = this.folds.then(async () => {
      try {
        const written = await setFold(relay, room, fold);
        if (--this.foldsPending === 0 && !this.stopped) {
          this.set({ threads: written.settings }); // with any other window's folds
        }
      } catch (err: unknown) {
        this.foldsPending--;
        this.fail(err);
        this.readKeys(); // the folds as the file has them
      }
    });
  }

  /** expandThreads lists all of room's threads, past the cap, until it is left: shown, if it was not. */
  expandThreads(room: string): void {
    this.peek(room);
    if (this.view.open === room) {
      this.expanded = room;
      this.set({});
    }
  }

  /**
   * markRoomAndThreadsRead marks a room read without opening it and each of its unread
   * threads by its own receipt at its newest reply (rooms.mark_all_read), as the TUI's does.
   */
  markRoomAndThreadsRead(room: string): void {
    const run = this.run;
    if (run === undefined || !this.holds(run, room)) {
      return;
    }
    const threads = (this.view.unread.get(room)?.threads ?? [])
      .filter((t) => t.latest !== "")
      .map((t) => ({ room, root: t.root, latest: t.latest }));
    this.markRooms(run, [room], run.joined.find((r) => r.id === room)?.name || room, threads);
  }

  /**
   * markThreadRowRead marks the thread of the room list's row (room, root) read at its newest
   * reply without opening it (rooms.mark_read on a thread's row), as the TUI's does: announced
   * or private as its room's receipt is.
   */
  markThreadRowRead(room: string, root: string): void {
    const run = this.run;
    const row = this.view.list.find((r) => r.kind === "thread" && r.room.id === room && r.thread.root === root);
    if (run === undefined || row?.kind !== "thread" || row.thread.latest === "" || !this.holds(run, room)) {
      return;
    }
    const title = row.thread.title.replace(/\s+/g, " ").trim() || "an older thread";
    relay
      .read()
      .then(
        ({ settings }) => settings,
        () => undefined,
      )
      .then((settings) =>
        call(backend.markThreadRead, {
          roomId: room,
          root,
          eventId: row.thread.latest,
          private: !announces(settings, room, ""),
        }),
      )
      .then(
        () => {
          if (this.live(run)) {
            this.set(this.noticed(`${title} is read`, "message"));
          }
        },
        (err: unknown) => {
          if (this.live(run)) {
            this.set(this.noticed(`${title} was not marked read: ${errorText(err)}`, "error"));
          }
        },
      );
  }

  /**
   * toggleRoomUnread marks a room unread, or clears its mark (rooms.mark_unread), as the
   * TUI's does; the badge follows from the unread stream. A thread's row has no mark.
   */
  toggleRoomUnread(room: string, root: string): void {
    const run = this.run;
    if (run === undefined || !this.holds(run, room)) {
      return;
    }
    if (root !== "") {
      // m.marked_unread is room account data; there is no per-thread form.
      this.set(this.noticed("A thread cannot be marked unread, only its room", "warning"));
      return;
    }
    const name = run.joined.find((r) => r.id === room)?.name || room;
    const unread = this.view.unread.get(room)?.marked !== true;
    call(backend.markRoomUnread, { roomId: room, unread }).then(
      () => {
        if (this.live(run)) {
          this.set(this.noticed(unread ? `${name} is marked unread` : `${name} is no longer marked unread`, "message"));
        }
      },
      (err: unknown) => {
        if (this.live(run)) {
          this.set(this.noticed(`${name} was not marked: ${errorText(err)}`, "error"));
        }
      },
    );
  }

  /**
   * askMarkRead asks before marking a rail row's unread rooms read (rail.mark_read), the
   * rooms captured with the question so a row that changes meanwhile cannot redirect it.
   */
  askMarkRead(row: number): void {
    const run = this.run;
    const place = this.view.rail[row];
    if (run === undefined || place === undefined) {
      return;
    }
    const rooms = [...new Set(place.rooms)].filter((id) => this.holds(run, id) && hasUnread(this.view.unread.get(id)));
    if (rooms.length === 0) {
      this.set(this.noticed(`Nothing is unread in ${place.label}`, "warning"));
      return;
    }
    this.set({ marking: { label: place.label, rooms } });
  }

  closeMarking(): void {
    this.set({ marking: undefined });
  }

  /** markAsked marks the rooms asked about read. */
  markAsked(): void {
    const run = this.run;
    const asked = this.view.marking;
    this.set({ marking: undefined });
    if (run !== undefined && asked !== undefined) {
      this.markRooms(run, asked.rooms, asked.label);
    }
  }

  /**
   * markRooms has kithd receipt each room's newest message (MarkRoomsRead), announced
   * where the settings say so, else private, and each of threads at its newest reply as its
   * room's is; the badges clear by the unread stream, and the line under the rooms says what
   * came of it.
   */
  private markRooms(
    run: Run,
    rooms: readonly string[],
    label: string,
    threads: readonly { room: string; root: string; latest: string }[] = [],
  ): void {
    // Settings that cannot be read leave every receipt private, as an open room's.
    relay
      .read()
      .then(
        ({ settings }) => settings,
        () => undefined,
      )
      .then((settings) => {
        const loud = rooms.filter((id) => announces(settings, id, ""));
        const quiet = rooms.filter((id) => !loud.includes(id));
        return Promise.all([
          Promise.all(
            [loud, quiet].flatMap((ids, i) =>
              ids.length === 0 ? [] : [call(backend.markRoomsRead, { roomIds: ids, private: i === 1 })],
            ),
          ),
          ...threads.map((t) =>
            call(backend.markThreadRead, {
              roomId: t.room,
              root: t.root,
              eventId: t.latest,
              private: !loud.includes(t.room),
            }),
          ),
        ]).then(([got]) => got);
      })
      .then(
        (got) => {
          if (this.live(run)) {
            this.set(this.noticed(readNote(rooms.length, label, got), readLevel(got)));
          }
        },
        (err: unknown) => {
          if (this.live(run)) {
            this.set(this.noticed(`${label} was not marked read: ${errorText(err)}`, "error"));
          }
        },
      );
  }

  /** forgetNotice takes the bar's note down. */
  forgetNotice(): void {
    clearTimeout(this.noticeLater);
    this.set({ notice: undefined });
  }

  /** noticed is text on the bar at level, going by itself after the level's [gui.notices] seconds. */
  private noticed(text: string, level: Level): { notice: Notice } {
    const notice = { text, level };
    clearTimeout(this.noticeLater);
    const seconds = noticeSeconds(this.view.theme, level);
    if (seconds > 0) {
      this.noticeLater = setTimeout(() => {
        if (this.view.notice === notice) {
          this.set({ notice: undefined });
        }
      }, seconds * 1000);
    }
    return { notice };
  }

  /** The People list (as the TUI's :people): every card the profile shows, narrowed by query. */
  openPeople(query = ""): void {
    if (this.run !== undefined) {
      this.people.openList(query);
    }
  }

  findPeople(query: string): void {
    this.people.find(query);
  }

  closePeople(): void {
    this.people.closeList();
  }

  /** listPeople is the cards a query meets (the switcher's field terms), as the profile shows them. */
  listPeople(query: string): Promise<readonly PersonListed[]> {
    return call(backend.people, { query }).then((r) => r.people);
  }

  /**
   * openPerson opens a person's card (as the TUI's I): a message's sender by their ID, a
   * card People listed, or a DM's one person; name is what the window calls them.
   */
  openPerson(who: Who, name: string): void {
    if (this.run !== undefined) {
      this.people.openCard(who, name);
    }
  }

  closePerson(): void {
    this.people.closeCard();
  }

  /** The open card's fields: edited, added, written (people.ts). */
  editField(key: string): void {
    this.people.edit(key);
  }

  typeField(text: string): void {
    this.people.typeEdit(text);
  }

  cancelField(): void {
    this.people.cancelEdit();
  }

  saveField(): void {
    this.people.save();
  }

  addField(): void {
    this.people.add();
  }

  pickFieldKey(key: string): void {
    this.people.pickKey(key);
  }

  nameFieldKey(): void {
    this.people.nameKey();
  }

  typeFieldKey(name: string): void {
    this.people.typeKeyName(name);
  }

  submitFieldKey(): void {
    this.people.submitKeyName();
  }

  /**
   * goToPerson opens your conversation with the person on a card People listed, as the
   * TUI's switcher does: your DM with them, or a room with them alone, in the profile;
   * else their card.
   */
  goToPerson(card: string): void {
    const run = this.run;
    if (run === undefined) {
      return;
    }
    call(backend.personCard, { card }).then(
      (r) => {
        if (!this.live(run)) {
          return;
        }
        if (r.conversation !== "" && this.holds(run, r.conversation)) {
          this.people.closeCard();
          this.people.closeList();
          this.open(r.conversation);
        } else {
          this.people.openCard({ card }, card);
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
   * searchSaid lists everything the person going by people said, in every room of the
   * profile (kithd's search, by sender): the TUI's "Everything they said".
   */
  searchSaid(name: string, people: readonly string[]): void {
    const run = this.run;
    if (run === undefined) {
      return;
    }
    const mine = ++run.searches;
    const shown = (s: Omit<Search, "query" | "scope" | "about">) => {
      run.search = { query: "", scope: "all", about: name, ...s };
      this.set({});
    };
    if (people.length === 0) {
      shown({ hits: [], busy: false, note: `kith knows no ID of ${name}'s` });
      return;
    }
    shown({ hits: [], busy: true, note: "" });
    call(backend.searchMessages, { query: "", senders: [...people], allRooms: true, limit: searchLimit }).then(
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

  /**
   * applyFiling files the room as the picker's ticks say, asking kithd only for the
   * places ticked otherwise than they hold it: kithd writes the tags, archives on the
   * network and records releases; the rail follows when kithd says it changed. Refused,
   * the picker stays open with the ticks as they were, saying why.
   */
  applyFiling(): void {
    const run = this.run;
    const f = this.view.filing;
    if (run === undefined || f === undefined || f.busy || f.places === undefined) {
      return;
    }
    const ask = filingAsked(f);
    if (ask.tags.length === 0 && ask.spaces.length === 0) {
      this.closeFiling();
      this.set(this.noticed("No change.", "message"));
      return;
    }
    const mine = this.filings;
    this.set({ filing: { ...f, busy: true, note: "" } });
    const still = () => this.filings === mine && this.view.filing?.busy === true;
    call(backend.fileRoom, { roomId: f.room, tags: ask.tags, spaces: ask.spaces }).then(
      (r) => {
        if (this.live(run)) {
          this.set({ filing: still() ? undefined : this.view.filing, ...this.noticed(told(f, ask, r), toldLevel(r)) });
        }
      },
      (err: unknown) => {
        if (!this.live(run)) {
          return;
        }
        if (still()) {
          this.set({ filing: { ...f, busy: false, note: errorText(err) } });
        } else {
          this.set(this.noticed(`${f.name} was not filed: ${errorText(err)}`, "error"));
        }
      },
    );
  }

  /** readAccounts reads the accounts a verification may be asked for from. */
  private readAccounts(run: Run): void {
    call(backend.accounts, {}).then(
      (r) => {
        if (this.live(run)) {
          this.set({ accounts: r.accounts });
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
   * verify asks this session's other sessions on account on to verify it, as the TUI's
   * :verify does: one of them accepts, then both show the emoji to compare.
   */
  verify(on: string): void {
    const run = this.run;
    if (run === undefined || this.view.verify.asking || this.view.verify.flow !== undefined) {
      return;
    }
    this.set({ verify: asking(this.view.verify) });
    call(backend.startVerification, { on }).then(
      (r) => {
        if (this.live(run)) {
          this.set({ verify: asked(this.view.verify, r.txnId) });
        }
      },
      (err: unknown) => {
        if (this.live(run)) {
          this.set({ verify: askFailed(this.view.verify, errorText(err)) });
        }
      },
    );
  }

  /** answerVerify accepts the request shown, or confirms the emoji shown match. */
  answerVerify(): void {
    const run = this.run;
    const f = this.view.verify.flow;
    if (run === undefined || !answerable(f)) {
      return;
    }
    const { txn, stage } = f;
    this.set({ verify: answering(this.view.verify) });
    const sent =
      stage === "requested"
        ? call(backend.acceptVerification, { txnId: txn })
        : call(backend.confirmSAS, { txnId: txn });
    const back = (error: string) => {
      if (this.live(run)) {
        this.set({ verify: heard(this.view.verify, txn, stage, error) });
      }
    };
    sent.then(
      () => {
        back("");
      },
      (err: unknown) => {
        back(errorText(err));
      },
    );
  }

  /**
   * cancelVerify declines the request shown, says the emoji do not match, or calls off
   * this window's own: shown as ended at once, as the TUI does, whatever kithd answers.
   */
  cancelVerify(): void {
    const f = this.view.verify.flow;
    if (this.run === undefined || f === undefined) {
      return;
    }
    this.set({ verify: canceled(this.view.verify) });
    call(backend.cancelVerification, { txnId: f.txn }).catch(() => undefined);
  }

  /** dismissVerify puts away what the last verification came to. */
  dismissVerify(): void {
    this.set({ verify: dismissed(this.view.verify) });
  }

  private readStatus(run: Run): void {
    call(backend.status, {}).then(
      (status) => {
        if (this.live(run)) {
          this.set({ status, problem: this.unsaved() });
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
    this.said++;
    this.set({ problem: err instanceof Error ? err.message : String(err) });
  }

  /** set changes the view, with the list and its count derived again, and tells who listens. */
  private set(change: Partial<View>): void {
    const next = { ...this.view, ...change };
    const run = this.run;
    const joined = run === undefined ? [] : ordered(run.joined, next.unread, run.at);
    next.rail = run?.rail?.rows ?? [];
    next.row = run?.rail === undefined ? -1 : placed(next.rail, run.row, run.rowAt);
    next.silenced = silencedRooms(run?.rail);
    next.rooms = listed(joined, run?.rail, next.row, next.open);
    next.joined = joined;
    next.unreadTotal = total(joined, next.unread, next.silenced);
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
    // Left, or gone from the list, the room whose threads were all listed is capped again.
    if (this.expanded !== next.open || !next.rooms.some((r) => r.id === this.expanded)) {
      this.expanded = "";
    }
    next.list = listRows(next.rooms, next.unread, run?.rail, next.threads, next.open, this.pinned(next), this.expanded);
    this.view = next;
    if (next.unreadTotal !== this.told) {
      this.told = next.unreadTotal;
      window.kith.unread(next.unreadTotal);
    }
    for (const listener of this.listeners) {
      listener();
    }
  }

  /** pinned are the open room's threads listed once read: the one open, and the one the list's cursor is on. */
  private pinned(next: View): ThreadUnread[] {
    const rows = next.timeline?.rows ?? [];
    const roots = [next.thread?.root ?? ""];
    if (this.cursor.room === next.open) {
      roots.push(this.cursor.root);
    }
    return roots.flatMap((root) => {
      const row = root === "" ? undefined : pinnedThread(rows, root);
      return row === undefined ? [] : [row];
    });
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

/**
 * spaceRooms is the rooms of room's home, as completion weighs them (tui spaceRoomsFor):
 * the first place on the rail holding it, a space or a tag; none, when it has none.
 */
function spaceRooms(rows: readonly RailRow[], room: string): string[] {
  const home = rows.find((r) => r.kind !== RailRowKind.ALL && r.rooms.includes(room));
  return home === undefined ? [] : [...home.rooms];
}

/** readNote says what marking rooms read came to, as the TUI's readStatus: one room named, a place counted. */
export function readNote(rooms: number, label: string, got: readonly MarkRoomsReadResponse[]): string {
  const marked = got.reduce((n, r) => n + r.marked, 0);
  const skipped = got.reduce((n, r) => n + r.skipped, 0);
  const failed = got.reduce((n, r) => n + r.failed, 0);
  const why = got.find((r) => r.firstError !== "")?.firstError ?? "";
  if (rooms === 1) {
    if (marked === 1) {
      return `${label} is read`;
    }
    if (skipped === 1) {
      return `Nothing of ${label} is cached yet: open it to mark it read`;
    }
    return `${label} was not marked read${why === "" ? "" : `: ${why}`}`;
  }
  const parts = [`${roomsPhrase(marked)} in ${label} marked read`];
  if (skipped > 0) {
    parts.push(`${roomsPhrase(skipped)} had nothing cached`);
  }
  if (failed > 0) {
    parts.push(`${roomsPhrase(failed)} refused${why === "" ? "" : `: ${why}`}`);
  }
  return parts.join(" · ");
}

/** readLevel is how much readNote's line matters: every room marked a message, none an error, some a warning. */
export function readLevel(got: readonly MarkRoomsReadResponse[]): Level {
  const marked = got.reduce((n, r) => n + r.marked, 0);
  if (got.every((r) => r.skipped === 0 && r.failed === 0)) {
    return "message";
  }
  return marked === 0 && got.some((r) => r.failed > 0) ? "error" : "warning";
}

/** toldLevel is how much a filing's line (told) matters: a warning where its network or a filter undid part of it. */
function toldLevel(r: FileRoomResponse): Level {
  return r.archiveFailed !== "" || r.releaseFailed !== "" ? "warning" : "message";
}

/** Level is how much a note on the bar matters: a message and a warning go by themselves, an error stays. */
export type Level = "message" | "warning" | "error";

/** Notice is a note on the bar, at its level. */
export interface Notice {
  text: string;
  level: Level;
}

/** noticeSeconds is how long a note at level stays, by [gui.notices] as theme has it (before it is read, its defaults); 0 or less, until dismissed. */
export function noticeSeconds(theme: ThemeResponse | undefined, level: Level): number {
  if (theme === undefined) {
    return { message: 5, warning: 10, error: -1 }[level];
  }
  return Number({ message: theme.messageSeconds, warning: theme.warningSeconds, error: theme.errorSeconds }[level]);
}

export function roomsPhrase(n: number): string {
  return n === 1 ? "1 room" : `${String(n)} rooms`;
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
