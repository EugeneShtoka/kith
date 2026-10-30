import { hostname } from "node:os";

import {
  create,
  type DescMethodServerStreaming,
  type DescMethodUnary,
  fromBinary,
  type MessageShape,
  toBinary,
} from "@bufbuild/protobuf";
import type { Transport } from "@connectrpc/connect";

import { BackendService, SeatHolderSchema, type SeatRequestSchema } from "../gen/backend/v1/services_pb";
import { DeviceService } from "../gen/device/v1/device_pb";
import type { StreamEvent } from "../shared/ipc";
import { runIn } from "../shared/profile";
import { keepOpen } from "./streams";

const backend = BackendService.method;
const device = DeviceService.method;

/**
 * The calls and streams the window may use, by name. An XSS in the window holds whatever
 * the preload holds, so this list is its scope: it grows with features, and a call that
 * reaches beyond reading and writing messages joins only with the PR that needs it.
 */
const calls = new Map<string, DescMethodUnary>(
  [
    backend.status,
    backend.rooms,
    backend.lastMessages,
    backend.cachedUnread,
    backend.cachedInvites,
    backend.joinRoom,
    backend.leaveRoom,
    device.device,
    device.chooseProfile,
  ].map((m) => [m.name, m]),
);
const streams = new Map<string, DescMethodServerStreaming>(
  [backend.changed, backend.configChanged, backend.seat, backend.unreadStream, backend.invites].map((m) => [m.name, m]),
);

/**
 * The calls the window may make only for a room the profile has an invitation to, by
 * the room each names: accepting (JoinRoom) and declining (LeaveRoom) one is all it
 * does with them, so a window run by someone else's message can neither join nor leave
 * any other room.
 */
const invitedOnly = new Map<string, (request: unknown) => string>([
  [backend.joinRoom.name, (r) => (r as MessageShape<typeof backend.joinRoom.input>).roomIdOrAlias],
  [backend.leaveRoom.name, (r) => (r as MessageShape<typeof backend.leaveRoom.input>).roomId],
]);

/** The window's origin: the app:// protocol main serves it from. */
export const appURL = "app://kith/";

/** What the gate reads of the frame a message came from (Electron's WebFrameMain). */
export interface Frame {
  url: string;
  parent: unknown;
}

/** A refusal: the window asked for what it may not have. */
export class Refused extends Error {}

/**
 * Gate is all the window reaches of kithd: the calls and streams on the list, from the
 * window's own top frame, with requests that decode as their method's. Every request to
 * kithd carries the profile the window runs in, which the gate follows from the relay's
 * answers (runIn), and none is sent before there is one: kithd would answer it for the
 * config's first profile. A stream opened in one profile is ended when the window
 * leaves it.
 */
export class Gate {
  private profile = "";
  private readonly open = new Set<{ profile: string; stop: () => void; post: (event: StreamEvent) => void }>();

  constructor(private readonly transport: Transport) {}

  /** call makes the call named and answers the response's bytes. */
  async call(frame: Frame | null, name: unknown, request: unknown): Promise<Uint8Array> {
    const method = admit(frame, calls, name);
    const input = decode(method, request);
    const header = this.header(method);
    const invited = invitedOnly.get(method.name);
    if (invited !== undefined) {
      await this.mustBeInvited(invited(input), header);
    }
    const response = await this.transport.unary(method, undefined, undefined, header, input);
    this.follow(method, response.message);
    return toBinary(method.output, response.message);
  }

  /** subscribe keeps the stream named open, posting its events, until the stop it returns. */
  subscribe(frame: Frame | null, name: unknown, request: unknown, post: (event: StreamEvent) => void): () => void {
    const method = admit(frame, streams, name);
    const input = decode(method, request);
    const header = this.header(method);
    let again: typeof input = input;
    if (method.name === backend.seat.name) {
      // The seat is asked for from this machine, whatever the window says; and a take by
      // force is forced once: opened again (kithd restarted), it is asked for, so a
      // window that took it meanwhile keeps it (daemon.Remote.keepSeat).
      const seat = input as MessageShape<typeof SeatRequestSchema>;
      seat.where = create(SeatHolderSchema, {
        pid: BigInt(process.pid),
        host: hostname(),
        sinceUnixMs: seat.where?.sinceUnixMs ?? 0n,
      });
      const asked: MessageShape<typeof SeatRequestSchema> = { ...seat, force: false };
      again = asked;
    }
    const stop = keepOpen(this.transport, method, input, header, post, again);
    const held = { profile: this.profile, stop, post };
    this.open.add(held);
    return () => {
      this.open.delete(held);
      stop();
    };
  }

  /**
   * restart is the window loading afresh (a reload): it runs in no profile until it
   * reads the device again, as a window just started does, so both decide alike.
   */
  restart(): void {
    this.use("");
  }

  /** header is what a request for method carries: kithd's the window's profile, refused without one. */
  private header(method: DescMethodUnary | DescMethodServerStreaming): HeadersInit {
    if (method.parent.typeName === DeviceService.typeName) {
      return {}; // the relay's own, in no profile
    }
    if (this.profile === "") {
      throw new Refused(`kith: no profile is chosen yet, so ${method.name} would run in the config's first`);
    }
    return { "Kith-Profile": this.profile };
  }

  /** follow moves the window to the profile a DeviceService answer leaves it in. */
  private follow(method: DescMethodUnary, message: unknown): void {
    if (method.name === device.device.name) {
      const d = message as MessageShape<typeof device.device.output>;
      this.use(runIn(this.profile, d.profile, d.profiles));
    } else if (method.name === device.chooseProfile.name) {
      this.use((message as MessageShape<typeof device.chooseProfile.output>).profile);
    }
  }

  /** use runs the window in profile from now on, ending every stream opened in another. */
  private use(profile: string): void {
    if (profile === this.profile) {
      return;
    }
    this.profile = profile;
    for (const held of this.open) {
      if (held.profile !== profile) {
        this.open.delete(held);
        held.stop();
        held.post({ kind: "refused", error: `kith: the window left the profile ${held.profile}` });
      }
    }
  }

  /** mustBeInvited refuses unless the profile has an invitation to room now. */
  private async mustBeInvited(room: string, header: HeadersInit): Promise<void> {
    const method = backend.cachedInvites;
    const invites = await this.transport.unary(method, undefined, undefined, header, create(method.input));
    if (room === "" || !invites.message.invites.some((r) => r.id === room)) {
      throw new Refused(`kith: the window may join or leave only a room it is invited to, not ${room}`);
    }
  }
}

/** admit is the method named, if the window may use it from frame. */
function admit<M>(frame: Frame | null, allowed: Map<string, M>, name: unknown): M {
  if (!fromTheWindow(frame)) {
    throw new Refused(`kith: a request from ${frame?.url ?? "a closed frame"}, not the window`);
  }
  const method = typeof name === "string" ? allowed.get(name) : undefined;
  if (method === undefined) {
    throw new Refused(`kith: the window may not use ${String(name)}`);
  }
  return method;
}

/** fromTheWindow: the frame is the window's top frame, on its own origin. */
export function fromTheWindow(frame: Frame | null): boolean {
  if (frame === null || frame.parent !== null) {
    return false;
  }
  try {
    const url = new URL(frame.url);
    return url.protocol === "app:" && url.host === "kith";
  } catch {
    return false;
  }
}

/**
 * decode is request as method's input, refused unless it is bytes that decode as one.
 * Fields the input does not have are dropped, so kithd is sent the method's fields alone.
 */
function decode(method: DescMethodUnary | DescMethodServerStreaming, request: unknown) {
  if (!(request instanceof Uint8Array)) {
    throw new Refused(`kith: the ${method.name} request is not bytes`);
  }
  try {
    return fromBinary(method.input, request, { readUnknownFields: false });
  } catch (err) {
    throw new Refused(`kith: the ${method.name} request does not decode: ${String(err)}`);
  }
}
