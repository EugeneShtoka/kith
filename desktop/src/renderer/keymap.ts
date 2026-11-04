// The window's keys, from the device's [keys] (DeviceSettings keys): each press named as
// the TUI names it, and sequences ("g g") held until they complete, as the TUI holds them.

/** A press as the browser reports it: the fields of a KeyboardEvent that name it. */
export interface Press {
  key: string;
  code: string;
  ctrlKey: boolean;
  altKey: boolean;
  shiftKey: boolean;
  metaKey: boolean;
}

/** named are the browser's names for the keys [keys] names by word (config's namedKeys). */
const named: Readonly<Record<string, string>> = {
  Enter: "enter",
  Escape: "esc",
  Tab: "tab",
  " ": "space",
  Backspace: "backspace",
  Delete: "delete",
  Insert: "insert",
  ArrowUp: "up",
  ArrowDown: "down",
  ArrowLeft: "left",
  ArrowRight: "right",
  Home: "home",
  End: "end",
  PageUp: "pgup",
  PageDown: "pgdown",
  ContextMenu: "menu",
  Pause: "pause",
  CapsLock: "capslock",
  NumLock: "numlock",
  ScrollLock: "scrolllock",
  PrintScreen: "printscreen",
};

/**
 * keyName is p as [keys] names it ("ctrl+k", "G", "shift+tab"), or undefined for a
 * modifier alone. A character is itself, shift in it; with ctrl, alt or super it is the
 * physical key's letter or digit, so a binding holds in any layout.
 * (internal/config/testdata/keynames.json holds this to Go's names.)
 */
export function keyName(p: Press): string | undefined {
  const word = named[p.key] ?? (/^F([1-9]|1\d|20)$/.test(p.key) ? p.key.toLowerCase() : undefined);
  const chord = p.ctrlKey || p.altKey || p.metaKey;
  let base: string;
  if (word !== undefined) {
    base = word;
  } else if (!/^.$/u.test(p.key)) {
    return undefined; // Shift, Control, Alt, Meta, Dead…
  } else if (!chord) {
    return p.key;
  } else {
    base = /^(?:Key[A-Z]|Digit\d)$/.test(p.code) ? p.code.slice(-1).toLowerCase() : p.key.toLowerCase();
  }
  const shift = p.shiftKey && (word !== undefined || chord);
  return (
    (p.ctrlKey ? "ctrl+" : "") + (p.altKey ? "alt+" : "") + (shift ? "shift+" : "") + (p.metaKey ? "super+" : "") + base
  );
}

/** typesText: name is a press that types a character into a text field. */
function typesText(name: string): boolean {
  return /^.$/u.test(name);
}

/** An action the window binds: its [keys] path, and whether it acts while a field takes typing. */
export interface Action {
  path: string;
  anywhere: boolean;
}

/** countLimit bounds a count typed before a motion ("12j"), as the TUI's does. */
const countLimit = 9999;

/**
 * Keymap resolves presses to actions. A sequence is held while it can still complete
 * (pending says so); a press that continues none ends it and is judged alone. Digits
 * that start no sequence are a count ("12j"), which the next action takes (count) and
 * any other press drops; a leading 0 is not one. Typing wins: while a field takes typing
 * only anywhere actions are consulted, each by a single press that types no text, and
 * nothing is held.
 */
export class Keymap {
  private readonly bound = new Map<string, Action>();
  private readonly prefixes = new Set<string>();
  private held: string[] = [];
  private typed = 0;
  private counted = 0;

  /** actions is the window's, in priority order: of two bound alike, the first acts. */
  constructor(keys: Readonly<Record<string, readonly string[]>>, actions: readonly Action[]) {
    for (const action of actions) {
      for (const seq of keys[action.path] ?? []) {
        if (this.bound.has(seq)) {
          continue;
        }
        this.bound.set(seq, action);
        const steps = seq.split(" ");
        for (let i = 1; i < steps.length; i++) {
          this.prefixes.add(steps.slice(0, i).join(" "));
        }
      }
    }
  }

  /** press is one named press: the path of the action it completes, if any. */
  press(name: string, typing: boolean): string | undefined {
    this.counted = 0;
    if (typing) {
      this.held = [];
      this.typed = 0;
      const action = this.bound.get(name);
      return action?.anywhere && !typesText(name) ? action.path : undefined;
    }
    const seq = [...this.held, name].join(" ");
    const action = this.bound.get(seq);
    if (action !== undefined) {
      this.held = [];
      this.counted = this.typed;
      this.typed = 0;
      return action.path;
    }
    if (this.prefixes.has(seq)) {
      this.held = [...this.held, name];
      return undefined;
    }
    if (this.held.length > 0) {
      this.held = [];
      return this.press(name, false);
    }
    if (/^\d$/.test(name) && (this.typed > 0 || name !== "0")) {
      this.typed = Math.min(this.typed * 10 + Number(name), countLimit);
      return undefined;
    }
    this.typed = 0;
    return undefined;
  }

  /** pending is the count and the sequence typed so far ("12", "g"), or "". */
  get pending(): string {
    return [...(this.typed > 0 ? [String(this.typed)] : []), ...this.held].join(" ");
  }

  /** count is the count the action press just returned was typed with, or 0 for none. */
  get count(): number {
    return this.counted;
  }
}

/**
 * moved is where a [keys.nav] motion takes a cursor at `at` among n rows, page of them
 * on screen, count typed before it (0: none), or undefined for a path that is no motion.
 * The ends with a count are that row ("12G"), as the TUI's are.
 */
export function moved(path: string, count: number, at: number, page: number, n: number): number | undefined {
  const by = (delta: number) => Math.max(0, Math.min(n - 1, at + delta));
  const half = Math.max(1, Math.floor(page / 2));
  switch (path) {
    case "nav.up":
      return by(-Math.max(count, 1));
    case "nav.down":
      return by(Math.max(count, 1));
    case "nav.page_up":
      return by(-Math.max(page, 1));
    case "nav.page_down":
      return by(Math.max(page, 1));
    case "nav.half_page_up":
      return by(-half);
    case "nav.half_page_down":
      return by(half);
    case "nav.select_oldest":
    case "nav.scroll_oldest":
      return count > 0 ? by(count - 1 - at) : 0;
    case "nav.select_newest":
    case "nav.scroll_newest":
      return count > 0 ? by(count - 1 - at) : n - 1;
  }
  return undefined;
}

/** typing: el takes typing (a text field, a text area, a select, an editable element). */
export function typing(el: Element | null): boolean {
  if (el instanceof HTMLInputElement) {
    return !["button", "checkbox", "radio", "range", "color", "file", "submit", "reset", "image"].includes(el.type);
  }
  return (
    el instanceof HTMLTextAreaElement ||
    el instanceof HTMLSelectElement ||
    (el instanceof HTMLElement && el.isContentEditable)
  );
}
