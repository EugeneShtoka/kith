import { hostname } from "node:os";

import {
  create,
  type DescMessage,
  type DescMethodServerStreaming,
  type DescMethodUnary,
  fromBinary,
  type MessageInitShape,
  type MessageShape,
  toBinary,
} from "@bufbuild/protobuf";
import { Code, ConnectError, type Transport } from "@connectrpc/connect";

import {
  BackendService,
  MessagesResponseSchema,
  SeatHolderSchema,
  type SeatRequestSchema,
} from "../gen/backend/v1/services_pb";
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
    backend.rail,
    backend.rooms,
    backend.lastMessages,
    backend.cachedUnread,
    backend.cachedInvites,
    backend.joinRoom,
    backend.leaveRoom,
    backend.cachedTimeline,
    backend.timeline,
    backend.messagesAround,
    backend.fetchEvent,
    backend.cachedReactions,
    backend.markRead,
    // The composer: sending, editing and deleting, reactions, typing, mentions, the
    // emoji ranking, and drafts (written as the seat's, below).
    backend.send,
    backend.redact,
    backend.sendReaction,
    backend.sendTyping,
    backend.mentionCandidates,
    backend.emojiScores,
    backend.recordEmoji,
    backend.selves,
    backend.drafts,
    backend.replaceDraft,
    // Search: kithd answers it in the profile's rooms only.
    backend.searchMessages,
    device.device,
    device.chooseProfile,
    device.deviceSettings,
  ].map((m) => [m.name, m]),
);
const streams = new Map<string, DescMethodServerStreaming>(
  [
    backend.changed,
    backend.configChanged,
    backend.seat,
    backend.unreadStream,
    backend.invites,
    backend.messages,
    backend.reactions,
  ].map((m) => [m.name, m]),
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
  /** The client each Seat stream open asks the seat for, in its profile. */
  private readonly seats = new Map<object, { profile: string; client: string }>();

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
    if (method.name === backend.replaceDraft.name) {
      this.asTheSeat(input as MessageShape<typeof backend.replaceDraft.input>);
    }
    const response = await this.transport.unary(method, undefined, undefined, header, input).catch((err: unknown) => {
      // A message gone from its network, or one kithd holds no copy of, is an answer: the
      // window is answered no message, and its quote shows as not loaded. A rejection
      // here is logged by Electron as an error.
      if (method.name === backend.fetchEvent.name && ConnectError.from(err).code === Code.NotFound) {
        return undefined;
      }
      throw err;
    });
    if (response === undefined) {
      return toBinary(method.output, create(method.output));
    }
    this.follow(method, response.message);
    return toBinary(method.output, response.message);
  }

  /** subscribe keeps the stream named open, posting its events, until the stop it returns. */
  subscribe(frame: Frame | null, name: unknown, request: unknown, post: (event: StreamEvent) => void): () => void {
    const method = admit(frame, streams, name);
    const input = decode(method, request);
    const header = this.header(method);
    let again = () => input;
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
      again = () => asked;
    }
    const seated = method.name === backend.seat.name ? {} : undefined;
    if (seated !== undefined) {
      this.seats.set(seated, {
        profile: this.profile,
        client: (input as MessageShape<typeof SeatRequestSchema>).client,
      });
    }
    if (method.name === backend.messages.name) {
      // Opened again (kithd restarted, the relay failed), the stream resumes after the
      // last change posted to the window, so it is sent what it missed meanwhile, once
      // (design-replay.md). A window loading afresh subscribes afresh, from the newest.
      let at: MessageShape<typeof backend.messages.input> | undefined;
      again = () => at ?? input;
      post = resuming(post, (epoch, change) => {
        at = create(backend.messages.input, { sinceEpoch: epoch, sinceChange: change });
      });
    }
    const stop = keepOpen(this.transport, method, input, header, post, again);
    const held = {
      profile: this.profile,
      stop: () => {
        if (seated !== undefined) {
          this.seats.delete(seated);
        }
        stop();
      },
      post,
    };
    this.open.add(held);
    return () => {
      this.open.delete(held);
      held.stop();
    };
  }

  /**
   * own makes a call main needs for the window (an attachment's upload, readying the
   * file), in the window's profile; the window cannot ask for these itself.
   */
  async own<I extends DescMessage, O extends DescMessage>(
    method: DescMethodUnary<I, O>,
    input: MessageInitShape<I>,
    signal: AbortSignal,
  ): Promise<MessageShape<O>> {
    const header = this.header(method);
    const response = await this.transport.unary(method, signal, undefined, header, create(method.input, input));
    return response.message;
  }

  /**
   * media is the bytes of a message's attachment, in the window's profile (LoadImage):
   * what kith-media:// pictures and saved files are. The window does not call LoadImage
   * itself; main does, for it.
   */
  async media(room: string, event: string): Promise<Uint8Array> {
    const method = backend.loadImage;
    const request = create(method.input, { roomId: room, eventId: event });
    const response = await this.transport.unary(method, undefined, undefined, this.header(method), request);
    return response.message.data;
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

  /**
   * asTheSeat names the window's seat in this profile on a draft write, whatever the
   * window says: kithd refuses it once another window has the seat (daemon seats.write),
   * so a window that stepped aside never writes over the one that took over. A window
   * asking for no seat here writes none.
   */
  private asTheSeat(request: MessageShape<typeof backend.replaceDraft.input>): void {
    const seat = [...this.seats.values()].find((s) => s.profile === this.profile);
    if (seat === undefined || seat.client === "") {
      throw new Refused(`kith: the window writes drafts only from its seat in ${this.profile}`);
    }
    request.client = seat.client;
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

/**
 * resuming posts a Messages stream's frames, telling held each one's position as it is
 * posted (the window holds it from then on). A frame saying to read again is posted as a
 * reload, in its place among the frames.
 */
function resuming(post: (event: StreamEvent) => void, held: (epoch: string, change: bigint) => void) {
  return (event: StreamEvent) => {
    if (event.kind !== "frames") {
      post(event);
      return;
    }
    let run: Uint8Array[] = [];
    for (const bytes of event.frames) {
      const frame = fromBinary(MessagesResponseSchema, bytes);
      if (frame.readAgain) {
        if (run.length > 0) {
          post({ kind: "frames", frames: run });
          run = [];
        }
        post({ kind: "reload" });
      } else {
        run.push(bytes);
      }
      held(frame.epoch, frame.change);
    }
    if (run.length > 0) {
      post({ kind: "frames", frames: run });
    }
  };
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
