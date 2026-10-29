import { type DescMethodServerStreaming, type DescMethodUnary, fromBinary, toBinary } from "@bufbuild/protobuf";
import type { Transport } from "@connectrpc/connect";

import { BackendService } from "../gen/backend/v1/services_pb";
import { DeviceService } from "../gen/device/v1/device_pb";
import type { StreamEvent } from "../shared/ipc";
import { keepOpen } from "./streams";

/**
 * The calls and streams the window may use, by name. An XSS in the window holds whatever
 * the preload holds, so this list is its scope: it grows with features, and a call that
 * reaches beyond reading and writing messages joins only with the PR that needs it.
 */
const calls = new Map<string, DescMethodUnary>(
  [BackendService.method.status, DeviceService.method.device].map((m) => [m.name, m]),
);
const streams = new Map<string, DescMethodServerStreaming>([BackendService.method.changed].map((m) => [m.name, m]));

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
 * window's own top frame, with requests that decode as their method's. Every request
 * carries the device's profile.
 */
export class Gate {
  constructor(
    private readonly transport: Transport,
    private readonly header: HeadersInit,
  ) {}

  /** call makes the call named and answers the response's bytes. */
  async call(frame: Frame | null, name: unknown, request: unknown): Promise<Uint8Array> {
    const method = admit(frame, calls, name);
    const input = decode(method, request);
    const response = await this.transport.unary(method, undefined, undefined, this.header, input);
    return toBinary(method.output, response.message);
  }

  /** subscribe keeps the stream named open, posting its events, until the stop it returns. */
  subscribe(frame: Frame | null, name: unknown, request: unknown, post: (event: StreamEvent) => void): () => void {
    const method = admit(frame, streams, name);
    return keepOpen(this.transport, method, decode(method, request), this.header, post);
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
function fromTheWindow(frame: Frame | null): boolean {
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
