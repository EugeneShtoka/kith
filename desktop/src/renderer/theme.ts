// The theme: light, dark or the system's, each mode's role colors, and a color per account
// and per person. The relay resolves it (Theme): each key from this device's device.toml,
// else the bridge's config.toml, else its default, and hands the TUI's colors the
// defaults come from. Here it becomes the stylesheet the window draws by, and a change
// of one key is written as the settings page writes one (settings.ts): over the files
// the page read, read again and written onto when another writer changed one, unless
// that writer changed the same key.
import { type ThemeResponse, ThemeScope, ThemeSource } from "../gen/device/v1/device_pb";
import type { ThemeMode } from "../shared/ipc";
import { hueOf } from "./faces";
import { aborted } from "./settings";

/** The roles a mode colors, and the token (styles.css) each one sets. */
export const roles = {
  accent: "--accent",
  background: "--bg",
  surface: "--surface",
  text: "--text",
  muted: "--muted",
  link: "--link",
  selection: "--accent-soft",
  unread: "--unread",
  mention: "--mention",
} as const;
export type Role = keyof typeof roles;

/** themeLabel is what the page calls key in what it says of a change. */
export function themeLabel(key: string): string {
  const [table = "", rest = ""] = key.split(/\.(.*)/su);
  switch (table) {
    case "mode":
      return "The mode";
    case "light":
    case "dark":
      return `The ${table} ${rest} color`;
    case "accounts":
      return `The color of account ${rest}`;
    default:
      return `The color of ${rest}`;
  }
}

/** Where a write goes: every device (the bridge's config.toml), or this one (device.toml). */
export type Scope = ThemeScope.SHARED | ThemeScope.DEVICE;

/** A color as the theme holds one; anything else is never put in a stylesheet. */
const color = /^#[0-9a-f]{6}$/i;

/** valueOf is key's effective value in theme, "" for its default with none. */
export function valueOf(theme: ThemeResponse, key: string): string {
  return theme.values[key]?.value ?? "";
}

/** picked is whether a file sets key: false for its default. */
export function picked(theme: ThemeResponse, key: string): boolean {
  return (theme.values[key]?.source ?? ThemeSource.UNSPECIFIED) !== ThemeSource.UNSPECIFIED;
}

/** layer is what scope's file holds of key, "" for nothing. */
export function layer(theme: ThemeResponse, key: string, scope: Scope): string {
  const v = theme.values[key];
  return (scope === ThemeScope.DEVICE ? v?.device : v?.shared) ?? "";
}

/** modeOf is the theme's mode: as set, else following the system. */
export function modeOf(theme: ThemeResponse | undefined): ThemeMode {
  const mode = theme === undefined ? "" : valueOf(theme, "mode");
  return mode === "light" || mode === "dark" ? mode : "system";
}

/** colorClass is the class that draws a face or a mark in hex (one themeCSS defines). */
export function colorClass(hex: string): string {
  return `c-${hex.slice(1).toLowerCase()}`;
}

/** Slots are a room's remembered sender colors (SenderSlots), by the key the TUI keeps each sender under. */
export type Slots = ReadonlyMap<string, number>;

/** Faces is what a person's color is read from: the theme, and the open room's slots. */
export interface Faces {
  theme: ThemeResponse | undefined;
  slots: Slots;
}

/** faceClass is the class a person's face and name are drawn in: their color, else the window's hashed hue. */
export function faceClass(faces: Faces, sender: string): string {
  const c = senderColor(faces.theme, faces.slots, sender);
  return c === undefined || !color.test(c) ? `hue-${String(hueOf(sender))}` : colorClass(c);
}

/**
 * senderColor is sender's color: picked for them, else as the TUI draws them in the
 * room (their identity's pinned color, else their slot's in its palette); undefined
 * where neither says, which the window draws in its own hashed hue.
 */
export function senderColor(theme: ThemeResponse | undefined, slots: Slots, sender: string): string | undefined {
  if (theme === undefined) {
    return undefined;
  }
  const key = `people.${sender}`;
  if (picked(theme, key)) {
    return valueOf(theme, key);
  }
  const identity = theme.identities[sender];
  if (identity !== undefined && identity.color !== "") {
    return identity.color;
  }
  const slot = slots.get(identity?.key ?? sender);
  return slot === undefined ? undefined : theme.senderPalette[slot];
}

/**
 * roomColor is the color of a room's mark: its account's, picked; else its network's,
 * as the TUI names its rooms; undefined for neither. A room is an account's when its ID
 * starts with the account's (domain.AccountRooms).
 */
export function roomColor(theme: ThemeResponse | undefined, room: { id: string; network: string }): string | undefined {
  if (theme === undefined) {
    return undefined;
  }
  for (const key of Object.keys(theme.values)) {
    if (key.startsWith("accounts.") && room.id.startsWith(key.slice("accounts.".length)) && picked(theme, key)) {
      return valueOf(theme, key);
    }
  }
  return theme.networkColors[room.network];
}

/**
 * themeCSS is the stylesheet the theme draws by: each mode's roles set over the built-in
 * tokens (in its prefers-color-scheme, which main follows from the mode), and a class for
 * every color a face or a mark may take (colorClass).
 */
export function themeCSS(theme: ThemeResponse | undefined): string {
  if (theme === undefined) {
    return "";
  }
  const out: string[] = [];
  for (const mode of ["light", "dark"] as const) {
    const set = Object.entries(roles)
      .map(([role, token]) => [token, valueOf(theme, `${mode}.${role}`)] as const)
      .filter(([, value]) => color.test(value))
      .map(([token, value]) => `${token}: ${value};`);
    if (set.length > 0) {
      out.push(`@media (prefers-color-scheme: ${mode}) { :root { ${set.join(" ")} } }`);
    }
  }
  const colors = new Set<string>([
    ...theme.senderPalette,
    ...Object.values(theme.networkColors),
    ...Object.values(theme.identities).map((i) => i.color),
    ...Object.entries(theme.values)
      .filter(([key]) => key.startsWith("people.") || key.startsWith("accounts."))
      .map(([, v]) => v.value),
  ]);
  for (const hex of [...colors].filter((c) => color.test(c)).map((c) => c.toLowerCase())) {
    const name = colorClass(hex);
    out.push(`.${name} { --hue: ${hex}; }`, `.net.${name} { color: ${hex}; }`);
  }
  return out.join("\n");
}

/** What the settings page reaches of the relay: Theme and UpdateTheme. */
export interface ThemeRelay {
  read(): Promise<ThemeResponse>;
  write(base: ThemeResponse, key: string, scope: Scope, value: string): Promise<ThemeResponse>;
}

/** The theme once a change is done; kept: it gave way to another writer's change of the same key. */
export interface ThemeChanged {
  theme: ThemeResponse;
  kept: boolean;
}

/** How often one change is written, each try lost to another writer, before the page gives up. */
export const themeTries = 4;

/**
 * changeTheme writes key = now in scope's file, changed from what the page showed
 * (shown), on shown's revisions. Told a file changed since, it reads the theme again:
 * if that file still holds what the page showed of key, the change is written onto it
 * as it is now, at most themeTries times; if another writer changed key there, theirs is
 * kept. A failure other than ABORTED, or the last try's, is thrown.
 */
export async function changeTheme(
  relay: ThemeRelay,
  shown: ThemeResponse,
  key: string,
  scope: Scope,
  now: string,
): Promise<ThemeChanged> {
  const was = layer(shown, key, scope);
  let base = shown;
  for (let tries = 1; ; tries++) {
    if (layer(base, key, scope) === now) {
      return { theme: base, kept: false };
    }
    if (layer(base, key, scope) !== was) {
      return { theme: base, kept: true };
    }
    try {
      return { theme: await relay.write(base, key, scope, now), kept: false };
    } catch (err) {
      if (!aborted(err) || tries >= themeTries) {
        throw err;
      }
    }
    base = await relay.read();
  }
}
