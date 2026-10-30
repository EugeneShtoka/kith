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

import type { KithBridge } from "../shared/ipc";

declare global {
  interface Window {
    readonly kith: KithBridge;
  }
}

/** A stream's events, its frames decoded. */
export type StreamEvent<O> =
  | { kind: "attached" }
  | { kind: "frames"; frames: O[] }
  | { kind: "reload" }
  | { kind: "detached"; error: string }
  | { kind: "refused"; error: string };

/** call makes a call through main, typed by its method. */
export async function call<I extends DescMessage, O extends DescMessage>(
  method: DescMethodUnary<I, O>,
  input: MessageInitShape<I>,
): Promise<MessageShape<O>> {
  const response = await window.kith.call(method.name, toBinary(method.input, create(method.input, input)));
  return fromBinary(method.output, response);
}

/** subscribe opens a stream through main, typed by its method; it returns what closes it. */
export function subscribe<I extends DescMessage, O extends DescMessage>(
  method: DescMethodServerStreaming<I, O>,
  input: MessageInitShape<I>,
  listener: (event: StreamEvent<MessageShape<O>>) => void,
): () => void {
  return window.kith.subscribe(method.name, toBinary(method.input, create(method.input, input)), (event) => {
    listener(
      event.kind === "frames"
        ? { kind: "frames", frames: event.frames.map((f) => fromBinary(method.output, f)) }
        : event,
    );
  });
}
