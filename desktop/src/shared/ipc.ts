// What crosses between main, the preload and the window: types and channel names only,
// so the preload links nothing else.

/** The IPC channels the preload uses, and main listens on. */
export const channels = {
  /** A call: (method name, request in protobuf binary), answered with the response's. */
  call: "kith:call",
  /** A stream: { method, request } with a MessagePort its events come on. */
  subscribe: "kith:subscribe",
  /** The profile's unread count, for the tray and the dock badge. */
  unread: "kith:unread",
} as const;

/** What a stream's port carries, in order. */
export type StreamEvent =
  /** kithd opened the stream: read again whatever it changes. */
  | { kind: "attached" }
  /** Frames in protobuf binary, batched: a burst is one message. */
  | { kind: "frames"; frames: Uint8Array[] }
  /** The stream ended or could not open; main opens it again, after a wait. */
  | { kind: "detached"; error: string }
  /** The window may not open this stream; the port closes after this. */
  | { kind: "refused"; error: string };

/** The subscribe message: which stream, and its request in protobuf binary. */
export interface Subscribe {
  method: string;
  request: Uint8Array;
}

/** window.kith: all the window can reach of main. */
export interface KithBridge {
  /** Makes the call named, if the window may; resolves to the response's bytes. */
  call(method: string, request: Uint8Array): Promise<Uint8Array>;
  /** Opens the stream named, if the window may; returns what closes it. */
  subscribe(method: string, request: Uint8Array, listener: (event: StreamEvent) => void): () => void;
  /** Says how many unread the profile's rooms hold, for the tray and the dock badge. */
  unread(count: number): void;
}
