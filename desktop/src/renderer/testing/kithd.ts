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

import { BackendService, type SeatRequest } from "../../gen/backend/v1/services_pb";
import { type Room, RoomSchema, type Unread, UnreadSchema } from "../../gen/backend/v1/types_pb";
import { DeviceService } from "../../gen/device/v1/device_pb";
import type { KithBridge, StreamEvent } from "../../shared/ipc";
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
  /** Answers held back until release, by method name. */
  readonly holding = new Set<string>();
  private held: (() => void)[] = [];
  private up = true;
  /** main's profile, as the gate keeps it. */
  private profile = "";

  of(profile: string): Profile {
    let p = this.data.get(profile);
    if (p === undefined) {
      p = { rooms: [], unread: new Map(), invites: [] };
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
    };
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
      this.send1(holder, backend.seat, { event: { case: "stepAside", value: true } });
    }
    this.seats.set(profile, "other");
  }

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
      this.seats.delete(sub.profile); // closing the seat's stream frees it
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
      case "LastMessages":
        return reply(backend.lastMessages, {});
      case "CachedUnread":
        return reply(backend.cachedUnread, { unread: [...p.unread.values()] });
      case "CachedInvites":
        return reply(backend.cachedInvites, { invites: p.invites });
      case "JoinRoom":
      case "LeaveRoom":
        return reply(backend.leaveRoom, {});
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
