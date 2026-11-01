// The bridge's settings on the settings page: config.toml's, every device's, one setting
// at a time. kithd lists what the window may set (ConfigSettings) and sets one over the
// file as the window read it (SetConfigSetting at its revision), answering ABORTED when
// another writer (the TUI's settings screen, a hand edit, another window) changed the
// file since. The page then reads the settings again and sets its one onto them, unless
// that setting was changed meanwhile: then it keeps the other writer's value and says
// so, rather than undo a change it never showed. Settings are read and written as kithd
// types them; the window never holds the config's text.
import { create, equals } from "@bufbuild/protobuf";

import {
  type ConfigSetting,
  type ConfigSettingsResponse,
  ConfigSettingKind,
  type ConfigValue,
  ConfigValueSchema,
  type SetConfigSettingResponse,
} from "../gen/backend/v1/services_pb";
import { aborted, changeTries } from "./settings";

/** What the page reaches of kithd: the settings, and one set (or cleared: undefined). */
export interface Bridge {
  read(): Promise<ConfigSettingsResponse>;
  write(base: string, path: string, value: ConfigValue | undefined): Promise<SetConfigSettingResponse>;
}

/** The settings once a change is done; kept: it gave way to another writer's change of the same setting. */
export interface Changed {
  read: ConfigSettingsResponse;
  kept: boolean;
}

/** The TUI's settings groups the bridge's settings are listed under, in its order, by its names. */
export const groups: readonly (readonly [string, string])[] = [
  ["notifications", "Notifications"],
  ["names", "Names"],
  ["display", "Messages"],
  ["rooms", "Rail and room list"],
  ["composer", "Composer"],
  ["media", "Images and media"],
  ["emoji", "Emoji and reactions"],
  ["look", "Look"],
  ["spelling", "Spelling"],
  ["completion", "Completion"],
  ["assist", "Assistant"],
  ["codes", "Codes and clipboard"],
  ["filters", "Filters"],
  ["agent", "AI agent access"],
  ["networks", "Accounts and networks"],
  ["advanced", "Commands, logs and schedule"],
];

/** grouped is the settings under each group that holds any, in the groups' order; one of a group not known last. */
export function grouped(
  settings: readonly ConfigSetting[],
): { group: string; title: string; settings: ConfigSetting[] }[] {
  const known = new Set(groups.map(([g]) => g));
  const out = groups.map(([group, title]) => ({
    group,
    title,
    settings: settings.filter((s) => s.group === group),
  }));
  const other = settings.filter((s) => !known.has(s.group));
  if (other.length > 0) {
    out.push({ group: "other", title: "Other", settings: other });
  }
  return out.filter((g) => g.settings.length > 0);
}

/** valueAt is what the file sets path to; undefined when it sets nothing there, or path is not listed. */
export function valueAt(read: ConfigSettingsResponse, path: string): ConfigValue | undefined {
  return read.settings.find((s) => s.path === path)?.value;
}

/** same is whether two values say the same: both unset, or equal (a list in order). */
export function same(a: ConfigValue | undefined, b: ConfigValue | undefined): boolean {
  if (a === undefined || b === undefined) {
    return a === b;
  }
  return equals(ConfigValueSchema, a, b);
}

/** withSetting is read with one setting as kithd answered it after a write, at the file's new revision. */
export function withSetting(read: ConfigSettingsResponse, written: SetConfigSettingResponse): ConfigSettingsResponse {
  const setting = written.setting;
  return {
    ...read,
    revision: written.revision,
    settings: read.settings.map((s) => (setting !== undefined && s.path === setting.path ? setting : s)),
  };
}

/**
 * change sets path to now (undefined clears it), changed from what the page showed
 * (shown), at shown's revision. Told the file changed since, it reads the settings
 * again: if path still holds what the page showed, the change is set onto the file as
 * it is now, at most changeTries times; if another writer changed path, theirs is kept.
 * A failure other than ABORTED, or the last try's, is thrown.
 */
export async function change(
  kithd: Bridge,
  shown: ConfigSettingsResponse,
  path: string,
  now: ConfigValue | undefined,
): Promise<Changed> {
  const was = valueAt(shown, path);
  let base = shown;
  for (let tries = 1; ; tries++) {
    if (same(valueAt(base, path), now)) {
      return { read: base, kept: false };
    }
    if (!same(valueAt(base, path), was)) {
      return { read: base, kept: true };
    }
    try {
      return { read: withSetting(base, await kithd.write(base.revision, path, now)), kept: false };
    } catch (err) {
      if (!aborted(err) || tries >= changeTries) {
        throw err;
      }
    }
    base = await kithd.read();
  }
}

/** flag, number, text and list are a setting's value of each kind. */
export function flag(on: boolean): ConfigValue {
  return create(ConfigValueSchema, { value: { case: "flag", value: on } });
}
export function number(n: bigint): ConfigValue {
  return create(ConfigValueSchema, { value: { case: "number", value: n } });
}
export function text(t: string): ConfigValue {
  return create(ConfigValueSchema, { value: { case: "text", value: t } });
}
export function list(entries: readonly string[]): ConfigValue {
  return create(ConfigValueSchema, { value: { case: "list", value: { entries: [...entries] } } });
}

/** inForce is a setting's value in force: the file's, else its default; undefined when neither says. */
export function inForce(s: ConfigSetting): ConfigValue | undefined {
  return s.value ?? s.fallback;
}

/** words is a value as the page shows it ("on", "2m", "a, b"), "none" for an empty list or text. */
export function words(v: ConfigValue | undefined): string {
  switch (v?.value.case) {
    case "flag":
      return v.value.value ? "on" : "off";
    case "number":
      return String(v.value.value);
    case "text":
      return v.value.value === "" ? "none" : v.value.value;
    case "list":
      return v.value.value.entries.length === 0 ? "none" : v.value.value.entries.join(", ");
    case undefined:
      return "not set";
  }
}

/** bounds is what a number setting may be, in words ("from 1 to 5"); "" for any. */
export function bounds(s: ConfigSetting): string {
  if (s.kind !== ConfigSettingKind.NUMBER) {
    return "";
  }
  if (s.least !== undefined && s.most !== undefined) {
    return `from ${String(s.least)} to ${String(s.most)}`;
  }
  if (s.least !== undefined) {
    return `${String(s.least)} or more`;
  }
  return s.most === undefined ? "" : `${String(s.most)} or less`;
}

/**
 * typed is what the page sends for a typed entry: a whole number within the setting's
 * bounds, a length of time as Go writes one, or text; undefined (clear) for nothing
 * typed. A string is why the entry is not one.
 */
export function typed(s: ConfigSetting, entry: string): ConfigValue | undefined | string {
  const t = entry.trim();
  if (t === "") {
    return undefined;
  }
  switch (s.kind) {
    case ConfigSettingKind.NUMBER: {
      if (!/^-?\d+$/.test(t)) {
        return `${t} is not a whole number`;
      }
      const n = BigInt(t);
      if ((s.least !== undefined && n < s.least) || (s.most !== undefined && n > s.most)) {
        return `${t} is not ${bounds(s)}`;
      }
      return number(n);
    }
    case ConfigSettingKind.DURATION:
      return duration.test(t) ? text(t) : `${t} is not a length of time: write 90s, 10m or 1h30m`;
    default:
      return text(entry);
  }
}

/** duration is a length of time as Go's time.ParseDuration reads one, not negative. */
const duration = /^(0|((\d+(\.\d*)?|\.\d+)(ns|us|µs|ms|s|m|h))+)$/;
