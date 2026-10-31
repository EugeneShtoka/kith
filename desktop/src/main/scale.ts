// The window's scale: its zoom (ctrl+= / ctrl+- / ctrl+0) and its text size. Only the
// window has them (decision 5), so main keeps them in Electron's own store,
// scale.json in userData, and applies them to the page itself.
import { readFile, rename, writeFile } from "node:fs/promises";

import { fontSizes } from "../shared/ipc";

export interface Scale {
  /** The zoom factor, one of zooms. */
  zoom: number;
  /** The root text size in pixels, one of fontSizes. */
  fontSize: number;
}

/** The zoom steps ctrl+= and ctrl+- move through, as a browser's. */
export const zooms = [0.5, 0.67, 0.75, 0.8, 0.9, 1, 1.1, 1.25, 1.5, 1.75, 2] as const;

export const defaultScale: Scale = { zoom: 1, fontSize: 15 };

/** valid is s read from the store, each key that is not one the window offers its default. */
export function valid(s: unknown): Scale {
  const got = typeof s === "object" && s !== null ? (s as Partial<Record<keyof Scale, unknown>>) : {};
  const one = (offered: readonly number[], v: unknown, otherwise: number) =>
    typeof v === "number" && offered.includes(v) ? v : otherwise;
  return {
    zoom: one(zooms, got.zoom, defaultScale.zoom),
    fontSize: one(fontSizes, got.fontSize, defaultScale.fontSize),
  };
}

/** A zoom key: ctrl (or cmd) with =, +, - or 0, as Electron's before-input-event gives it. */
export interface KeyInput {
  type: string;
  key: string;
  control: boolean;
  meta: boolean;
  alt: boolean;
}

/** zoomKey is what a key does to the zoom: in, out, back to 100%, or nothing. */
export function zoomKey(input: KeyInput): "in" | "out" | "reset" | undefined {
  if (input.type !== "keyDown" || !(input.control || input.meta) || input.alt) {
    return undefined;
  }
  switch (input.key) {
    case "=":
    case "+":
      return "in";
    case "-":
    case "_":
      return "out";
    case "0":
      return "reset";
    default:
      return undefined;
  }
}

/** zoomed is the zoom after a zoom key: the next step in or out, held at the ends. */
export function zoomed(zoom: number, key: "in" | "out" | "reset"): number {
  if (key === "reset") {
    return defaultScale.zoom;
  }
  const steps: readonly number[] = zooms;
  const at = steps.indexOf(zoom);
  const from = at >= 0 ? at : steps.indexOf(defaultScale.zoom);
  const to = Math.min(steps.length - 1, Math.max(0, from + (key === "in" ? 1 : -1)));
  return steps[to] ?? zoom;
}

/** fontSizeCSS sets the window's root text size; html:root outranks the stylesheet's :root. */
export function fontSizeCSS(px: number): string {
  return `html:root { --font-size: ${String(px)}px; }`;
}

/**
 * ScaleStore is scale.json: read once at start, each change written whole (to a file
 * beside it, then renamed over it), one write at a time and in order, so the last
 * change is what is left.
 */
export class ScaleStore {
  private scale = defaultScale;
  private writing: Promise<void> = Promise.resolve();

  constructor(private readonly file: string) {}

  /** load reads the store; one missing or unreadable is the defaults. */
  async load(): Promise<Scale> {
    try {
      this.scale = valid(JSON.parse(await readFile(this.file, "utf8")));
    } catch {
      this.scale = defaultScale;
    }
    return this.scale;
  }

  get(): Scale {
    return this.scale;
  }

  /** set changes the scale now, and resolves once it is on disk. */
  set(change: Partial<Scale>): Promise<void> {
    this.scale = valid({ ...this.scale, ...change });
    const text = JSON.stringify(this.scale) + "\n";
    const write = async () => {
      const part = `${this.file}.part`;
      await writeFile(part, text, { mode: 0o600 });
      await rename(part, this.file);
    };
    this.writing = this.writing.then(write, write);
    return this.writing;
  }
}
