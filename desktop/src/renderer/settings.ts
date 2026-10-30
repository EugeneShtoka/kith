// The settings page: a change to one of the device's settings, written to device.toml at
// once. The relay writes only over the file the page read (UpdateDeviceSettings at its
// revision) and answers ABORTED when another writer (the TUI's settings editor, a hand
// edit) changed it since. The page then reads the file again and writes its change onto
// that, unless the same setting was changed meanwhile: then it keeps the other writer's
// value and says so, rather than undo a change it never showed.
import { clone, create, equals } from "@bufbuild/protobuf";

import { type DeviceSettings, DeviceSettingsSchema } from "../gen/device/v1/device_pb";

/** A setting the page changes: each key DeviceSettings carries. */
export type Key = Exclude<keyof DeviceSettings, "$typeName" | "$unknown">;

/** The settings as the file holds them, and the revision of the file they were read from. */
export interface Read {
  settings: DeviceSettings;
  revision: string;
}

/** What the page reaches of the relay: DeviceSettings and UpdateDeviceSettings. */
export interface Device {
  read(): Promise<Read>;
  write(base: string, settings: DeviceSettings): Promise<Read>;
}

/** The settings once a change is done; kept: it gave way to another writer's change of the same setting. */
export interface Changed extends Read {
  kept: boolean;
}

/** How often one change is written, each try lost to another writer, before the page gives up. */
export const changeTries = 4;

/** What each setting is called on the page, and in what it says of one. */
export const labels: Record<Key, string> = {
  sendReceipts: "Send read receipts",
  readRules: "Read receipt rules",
  unreadLine: "Unread line",
  markdown: "Send Markdown rendered",
  confirmSms: "Ask before sending as SMS",
  sendTyping: "Send typing",
  skinTone: "Emoji skin tone",
  emojiSet: "Emoji set",
  emojiExtra: "Extra emoji shortcodes",
  reactionsScope: "Reaction palette ranking",
  reactionsStatic: "Reaction palette",
};

/** withKey is settings with key set to value. */
export function withKey<K extends Key>(settings: DeviceSettings, key: K, value: DeviceSettings[K]): DeviceSettings {
  const next = clone(DeviceSettingsSchema, settings);
  next[key] = value;
  return next;
}

/** same is whether two values of key say the same (a list in order, a map in any). */
export function same<K extends Key>(key: K, a: DeviceSettings[K], b: DeviceSettings[K]): boolean {
  const empty = create(DeviceSettingsSchema);
  return equals(DeviceSettingsSchema, withKey(empty, key, a), withKey(empty, key, b));
}

/** aborted is the relay refusing a write over a file changed since it was read (ABORTED, through main). */
export function aborted(err: unknown): boolean {
  return err instanceof Error && err.message.includes("[aborted]");
}

/**
 * change writes key = now, changed from what the page showed (shown), at shown's
 * revision. Told the file changed since, it reads it again: if key still holds what
 * the page showed, the change is written onto the file as it is now, at most
 * changeTries times; if another writer changed key, theirs is kept. A failure other
 * than ABORTED, or the last try's, is thrown.
 */
export async function change<K extends Key>(
  device: Device,
  shown: Read,
  key: K,
  now: DeviceSettings[K],
): Promise<Changed> {
  const was = shown.settings[key];
  let base = shown;
  for (let tries = 1; ; tries++) {
    if (same(key, base.settings[key], now)) {
      return { ...base, kept: false };
    }
    if (!same(key, base.settings[key], was)) {
      return { ...base, kept: true };
    }
    try {
      return { ...(await device.write(base.revision, withKey(base.settings, key, now))), kept: false };
    } catch (err) {
      if (!aborted(err) || tries >= changeTries) {
        throw err;
      }
    }
    base = await device.read();
  }
}
