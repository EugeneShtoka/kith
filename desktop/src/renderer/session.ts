// The window's state, outside React: the profile it runs in, its seat there, and the
// profile's rooms, unread and invitations, kept by the streams main holds open. React
// reads it through useSyncExternalStore; nothing here draws.
import type { DescMessage, DescMethodServerStreaming, MessageInitShape, MessageShape } from "@bufbuild/protobuf";

import {
  BackendService,
  Change,
  type ReactionsResponse,
  type SeatHolder,
  type SeatResponse,
  type StatusResponse,
} from "../gen/backend/v1/services_pb";
import type { Member, Message, Reaction, Room, Unread } from "../gen/backend/v1/types_pb";
import { type DeviceSettingsResponse, DeviceService } from "../gen/device/v1/device_pb";
import { runIn } from "../shared/profile";
import { Composer, type ComposerView, type Unsent } from "./compose";
import { call, subscribe } from "./kith";
import { announces } from "./read";
import { ordered, total } from "./rooms";
import { type Ask, newest, Timeline, type TimelineView } from "./timeline";

const backend = BackendService.method;

/** How many messages a network page asks for, and a cached page holds (as the TUI's). */
const networkPage = 50n;
const cachedPage = 2000n;
/** How many reply targets are fetched at once. */
const quoteFetches = 6;

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

/** View is everything the window draws. */
export interface View {
  /** The config's profiles, in order, and the one the window runs in: "" asks which. */
  profiles: readonly string[];
  profile: string;
  socket: string;
  seat: Seat;
  link: Link;
  status: StatusResponse | undefined;
  /** The profile's joined rooms, in list order, and its invitations. */
  rooms: readonly Room[];
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
  problem: string | undefined;
}

const nothingShown = {
  seat: { state: "none" },
  link: { state: "connecting" },
  status: undefined,
  rooms: [],
  invites: [],
  unread: new Map(),
  unreadTotal: 0,
  open: "",
  timeline: undefined,
  unreadLine: true,
  composer: undefined,
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
  /** Counts the room reads, so an older answer arriving last is dropped. */
  roomReads: number;
  /** The Messages stream has said where it starts: a page read from now misses no change. */
  positioned: boolean;
  /** The open room's timeline, and the device settings it follows: undefined while read. */
  timeline: Timeline | undefined;
  settings: DeviceSettingsResponse | "failed" | undefined;
  /** Counts the reactions reads; the Reactions frames held while one is answered. */
  reactionReads: number;
  reactionsHeld: ReactionsResponse[] | undefined;
  /** The composer: drafts, files, failed sends. */
  composer: Composer;
}

export class Session {
  private view: View = { profiles: [], profile: "", socket: "", problem: undefined, ...nothingShown };
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
  private told = 0;
  /** The open room is watched: shown at its newest, the window focused. */
  private watching = false;

  /** start reads the device, which runs the window in a profile or has it ask. */
  start(): void {
    this.readDevice();
    // Closing, the window writes its drafts first (main waits a little for it).
    window.kith.closing(() => this.run?.composer.drafts.flush() ?? Promise.resolve());
  }

  /** stop closes every stream: the window is going. */
  stop(): void {
    this.leave();
  }

  readonly subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };

  readonly snapshot = (): View => this.view;

  /** choose runs the window in the profile named, remembered for its next start. */
  choose(name: string): Promise<void> {
    return this.inTurn(async () => {
      const chosen = await call(DeviceService.method.chooseProfile, { name });
      this.enter(chosen.profile);
    });
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
   * open shows a room: its newest cached messages, then older ones as the window scrolls
   * back, kept by the Messages stream. The unread line follows the last message read as
   * it opened, and stays there while it is open.
   */
  open(room: string): void {
    const run = this.run;
    if (run === undefined || room === this.view.open) {
      return;
    }
    run.timeline = new Timeline(room, this.view.unread.get(room)?.readEvent ?? "");
    run.settings = undefined;
    run.composer.opened(room);
    this.set({ open: room, unreadLine: true });
    this.readSettings(run, run.timeline);
    this.readReactions(run);
    this.pump(run);
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

  /** readDevice reads the profiles again and moves the window where the answer leaves it. */
  private readDevice(): void {
    void this.inTurn(async () => {
      const d = await call(DeviceService.method.device, {});
      this.set({ profiles: d.profiles, socket: d.socket, problem: undefined });
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
    this.leave();
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
      }),
    };
    this.run = run;
    return run;
  }

  /** leave closes everything the run opened; its late answers are dropped. */
  private leave(): void {
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
            break;
          case "frames":
            if (e.frames.some((f) => f.change === Change.ROOMS)) {
              this.readRooms(run);
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
              }
            }
            // The first frame says where the stream starts: a page read after it misses
            // nothing, each change after it coming as a frame.
            run.positioned = true;
            this.pump(run);
            this.markRead(run);
            this.set({});
            break;
          case "reload":
            // kithd could not resume where main left off: frames were lost, so what is
            // shown is read again, and so are the rooms' last messages.
            run.positioned = true;
            run.timeline?.reload();
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

  /** readSettings reads the device settings the open room's receipts and unread line follow. */
  private readSettings(run: Run, t: Timeline): void {
    call(DeviceService.method.deviceSettings, {}).then(
      (settings) => {
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
          run.at = last.at;
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
    next.rooms = run === undefined ? [] : ordered(run.joined, next.unread, run.at);
    next.unreadTotal = total(next.rooms, next.unread);
    next.timeline = run?.timeline?.view();
    next.composer = run === undefined || next.open === "" ? undefined : run.composer.view(next.open);
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
