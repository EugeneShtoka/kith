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
  /** Saving an attachment: (room, event, name), answered with whether it was saved. */
  save: "kith:save",
  /** Files to send, picked in the open dialog: answered with what was offered. */
  pick: "kith:pick",
  /** A file dropped or pasted, by the path the system gave it (the preload's): an Offered. */
  offerPath: "kith:offer-path",
  /** Bytes pasted with no file behind them: (name, bytes), answered with an Offered. */
  offerBytes: "kith:offer-bytes",
  /** Taking an offered file back: (token). */
  discard: "kith:discard",
  /** Sending an offered file: (token, room, caption), answered with a Sent. */
  attach: "kith:attach",
  /** Main to the window: it is closing, write what is unsaved; the window answers closed. */
  closing: "kith:closing",
  closed: "kith:closed",
  /** The window's text size in pixels (Electron's own store, not device.toml): asked, or set to (px). */
  fontSize: "kith:font-size",
  setFontSize: "kith:set-font-size",
  /** The theme's mode ([gui.theme] mode), which main gives the window's light or dark: (mode). */
  themeMode: "kith:theme-mode",
} as const;

/** The text sizes the window offers, in pixels; the default is 15. */
export const fontSizes = [12, 13, 14, 15, 16, 17, 18, 20] as const;

/** The modes the theme takes: following the system's light or dark, or one of them. */
export const themeModes = ["system", "light", "dark"] as const;
export type ThemeMode = (typeof themeModes)[number];

/** A file offered for sending: main holds it, the window holds the token. */
export interface Offered {
  token: string;
  name: string;
  size: number;
}

/** A file sent; kept: with metadata kith cannot remove from it (HEIC). */
export interface Sent {
  kept: boolean;
}

/** What a stream's port carries, in order. */
export type StreamEvent =
  /** kithd opened the stream: read again whatever it changes. */
  | { kind: "attached" }
  /** Frames in protobuf binary, batched: a burst is one message. */
  | { kind: "frames"; frames: Uint8Array[] }
  /**
   * Messages only: kithd could not resume where the window left off, or its cache was
   * cleared. What the window holds may be stale: it reads it again, and the frames
   * after this continue from there.
   */
  | { kind: "reload" }
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
  /**
   * Saves a message's attachment where the person picks, name offered; resolves to
   * whether it was saved.
   */
  save(room: string, event: string, name: string): Promise<boolean>;
  /** Asks for files to send in the open dialog; resolves to those picked. */
  pick(): Promise<Offered[]>;
  /**
   * Offers a file dropped or pasted: main reads it where the system says it is, or takes
   * its bytes when it is no file on disk (a picture pasted). The window names no path.
   */
  offer(file: File): Promise<Offered>;
  /** Takes an offered file back. */
  discard(token: string): Promise<void>;
  /** Sends an offered file to room, caption with it. */
  attach(token: string, room: string, caption: string): Promise<Sent>;
  /** Says what to do as the window closes (write its drafts); main waits a little for it. */
  closing(write: () => Promise<void>): void;
  /** The window's text size in pixels, which main keeps and applies. */
  fontSize(): Promise<number>;
  /** Sets the window's text size (one of fontSizes); resolves to the size now in use. */
  setFontSize(px: number): Promise<number>;
  /** Draws the window light, dark, or as the system is (the theme's mode). */
  themeMode(mode: ThemeMode): void;
}
