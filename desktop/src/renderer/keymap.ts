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

/**
 * Keymap resolves presses to actions. A sequence is held while it can still complete
 * (pending says so); a press that continues none ends it and is judged alone. Typing
 * wins: while a field takes typing only anywhere actions are consulted, each by a single
 * press that types no text, and nothing is held.
 */
export class Keymap {
  private readonly bound = new Map<string, Action>();
  private readonly prefixes = new Set<string>();
  private held: string[] = [];

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
    if (typing) {
      this.held = [];
      const action = this.bound.get(name);
      return action?.anywhere && !typesText(name) ? action.path : undefined;
    }
    const seq = [...this.held, name].join(" ");
    const action = this.bound.get(seq);
    if (action !== undefined) {
      this.held = [];
      return action.path;
    }
    if (this.prefixes.has(seq)) {
      this.held = [...this.held, name];
      return undefined;
    }
    const wasHeld = this.held.length > 0;
    this.held = [];
    return wasHeld ? this.press(name, false) : undefined;
  }

  /** pending is the sequence held so far ("g"), or "". */
  get pending(): string {
    return this.held.join(" ");
  }
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
