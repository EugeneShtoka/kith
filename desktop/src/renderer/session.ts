// The window's state, outside React: the profile it runs in, its seat there, and the
// profile's rooms, unread and invitations, kept by the streams main holds open. React
// reads it through useSyncExternalStore; nothing here draws.
import type { DescMessage, DescMethodServerStreaming, MessageInitShape, MessageShape } from "@bufbuild/protobuf";

import {
  BackendService,
  Change,
  type Account,
  type ConfigSettingsResponse,
  type ConfigValue,
  type Device as PairedDevice,
  type FollowStreamResponse,
  type LoginNetwork,
  type PersonListed,
  type RailResponse,
  type RailRow,
  RailRowKind,
  type ReactionsResponse,
  type SeatHolder,
  type SeatResponse,
  type StatusResponse,
} from "../gen/backend/v1/services_pb";
import type { Member, Message, Reaction, Room, SearchHit, Unread } from "../gen/backend/v1/types_pb";
import { type DeviceSettings, DeviceService, type ThemeResponse } from "../gen/device/v1/device_pb";
import { runIn } from "../shared/profile";
import { later, listed, placed, refOf, type RowRef } from "./rail";
import { Composer, type ComposerView, type Unscheduled, type Unsent } from "./compose";
import { asked as filingAsked, type Filing, offered, opened, toggled, told } from "./filing";
import { arrived, type Jumplist, nowhere, step } from "./jumplist";
import { Devices, type DevicesCalls, type DevicesView, noDevices } from "./devices";
import { call, subscribe } from "./kith";
import { Login, type LoginCalls, type LoginView } from "./login";
import { type OfferCalls, Offers, type Prompt } from "./offers";
import { noPeople, People, type PeopleCalls, type PeopleView, type Who } from "./people";
import type { Asked } from "./polls";
import { type Bridge, change as changeConfig } from "./config";
import { changeTheme, type ThemeRelay, themeLabel, type Scope, type Slots } from "./theme";
import { announces } from "./read";
import { ordered, total } from "./rooms";
import type { When } from "./schedule";
import { emptyFilter, parseSearch } from "./search";
import { change, type Device, type Key, labels, type Read, withKey } from "./settings";
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
  /** The settings page's bridge settings: config.toml's, every device's (config.ts). */
  config: ConfigView;
  /** The open room's remembered sender colors (SenderSlots). */
  slots: Slots;
  /** The settings page's accounts: the networks to log in to, and a login under way. */
  login: LoginView;
  /** The settings page's devices: those paired, a pairing or a join under way (devices.ts). */
  devices: DevicesView;
  /** Verifying this session (verify.ts), and the accounts it is asked for from (Accounts). */
  verify: Verify;
  accounts: readonly Account[];
  /** The one-time offers (offers.ts): the one shown, and what the last download came to. */
  offers: { prompt: Prompt | undefined; note: string };
  /** The filing picker open on a room (filing.ts), and what the last filing came to. */
  filing: Filing | undefined;
  filingNote: string;
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

/** offerCalls is the one-time offers' calls: kithd's, through main, and device.toml's through the relay. */
const offerCalls: OfferCalls = {
  settings: () => relay.read().then((r) => r.settings),
  detectLanguages: () => call(backend.detectLanguages, {}),
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
  verify: noVerify,
  accounts: [],
  filing: undefined,
  filingNote: "",
  people: noPeople,
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
    offers: { prompt: undefined, note: "" },
    problem: undefined,
    ...nothingShown,
  };
  /** The one-time offers of dictionaries, word counts and the completion model: once a window. */
  private readonly offers = new Offers(offerCalls, () => {
    this.set({ offers: { prompt: this.offers.prompt, note: this.offers.note } });
  });
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
  /** Counts the problems said; and the last, when a page's: taken down once its reader reads. */
  private said = 0;
  private pageProblem: { reader: Timeline | OpenThread; said: number } | undefined;
  /** Counts the settings page's reads and writes: only the last one's answer is shown. */
  private settingsReads = 0;
  /** Counts the theme's reads and writes, likewise. */
  private themeReads = 0;
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

  /** The People list and a person's card, in the profile the window runs in. */
  private readonly people = new People(peopleCalls, (people) => {
    this.set({ people });
  });

  /** start reads the device, which runs the window in a profile or has it ask. */
  start(): void {
    this.stopped = false; // StrictMode starts a window again after stopping it
    this.readDevice();
    // Closing, the window writes its drafts first (main waits a little for it).
    window.kith.closing(() => this.run?.composer.drafts.flush() ?? Promise.resolve());
  }

  /** stop closes every stream: the window is going. */
  stop(): void {
    this.stopped = true;
    this.login.stop();
    this.devices.stop();
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
    this.set({ settingsPage: undefined, config: noConfig });
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
            this.readAccounts(run);
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
              this.readAccounts(run);
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
    this.set({ filing: opened(room, name || room), filingNote: "" });
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

  /** forgetFilingNote clears what the last filing came to. */
  forgetFilingNote(): void {
    this.set({ filingNote: "" });
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
      this.set({ filingNote: "No change." });
      return;
    }
    const mine = this.filings;
    this.set({ filing: { ...f, busy: true, note: "" } });
    const still = () => this.filings === mine && this.view.filing?.busy === true;
    call(backend.fileRoom, { roomId: f.room, tags: ask.tags, spaces: ask.spaces }).then(
      (r) => {
        if (this.live(run)) {
          this.set({ filing: still() ? undefined : this.view.filing, filingNote: told(f, ask, r) });
        }
      },
      (err: unknown) => {
        if (!this.live(run)) {
          return;
        }
        if (still()) {
          this.set({ filing: { ...f, busy: false, note: errorText(err) } });
        } else {
          this.set({ filingNote: `${f.name} was not filed: ${errorText(err)}` });
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

/**
 * spaceRooms is the rooms of room's home, as completion weighs them (tui spaceRoomsFor):
 * the first place on the rail holding it, a space or a tag; none, when it has none.
 */
function spaceRooms(rows: readonly RailRow[], room: string): string[] {
  const home = rows.find((r) => r.kind !== RailRowKind.ALL && r.rooms.includes(room));
  return home === undefined ? [] : [...home.rooms];
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
