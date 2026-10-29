// The window's state, outside React: the profile it runs in, its seat there, and the
// profile's rooms, unread and invitations, kept by the streams main holds open. React
// reads it through useSyncExternalStore; nothing here draws.
import type { DescMessage, DescMethodServerStreaming, MessageInitShape, MessageShape } from "@bufbuild/protobuf";

import {
  BackendService,
  Change,
  type SeatHolder,
  type SeatResponse,
  type StatusResponse,
} from "../gen/backend/v1/services_pb";
import type { Room, Unread } from "../gen/backend/v1/types_pb";
import { DeviceService } from "../gen/device/v1/device_pb";
import { runIn } from "../shared/profile";
import { call, subscribe } from "./kith";
import { ordered, total } from "./rooms";

const backend = BackendService.method;

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

  /** start reads the device, which runs the window in a profile or has it ask. */
  start(): void {
    this.readDevice();
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
    this.run = { profile, closed: false, seat: undefined, streams: [], joined: [], at: {}, roomReads: 0 };
    return this.run;
  }

  /** leave closes everything the run opened; its late answers are dropped. */
  private leave(): void {
    const run = this.run;
    this.run = undefined;
    if (run === undefined) {
      return;
    }
    run.closed = true;
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
        }
        break;
      case "taken":
        this.lose({ state: "taken", holder: frame.event.value });
        break;
      case "stepAside":
        // Closing the seat's stream is what lets the other window in.
        this.lose({ state: "steppedAside" });
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
