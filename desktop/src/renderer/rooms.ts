// The room list: the profile's joined rooms (those of the rail row the window is on,
// rail.ts), unread first, then by their last message, newest first.
import type { Room, Unread } from "../gen/backend/v1/types_pb";

/**
 * unreadOf is what a room's badge counts: the cache's own count when it could count
 * (the device default, `[display] unread = "messages"`), else the homeserver's.
 */
export function unreadOf(u: Unread | undefined): { count: number; mentions: number } {
  if (u === undefined) {
    return { count: 0, mentions: 0 };
  }
  return u.counted
    ? { count: Number(u.messages), mentions: Number(u.mentions) }
    : { count: Number(u.notifications), mentions: Number(u.highlights) };
}

/** hasUnread: the room has something to badge, or was marked unread. */
export function hasUnread(u: Unread | undefined): boolean {
  return u !== undefined && (u.marked || unreadOf(u).count > 0);
}

/**
 * ordered is rooms (invitations left out: they are listed apart) with something unread
 * first, then by when each last had a message (at, Unix milliseconds by room ID), newest
 * first, then by name.
 */
export function ordered(
  rooms: readonly Room[],
  unread: ReadonlyMap<string, Unread>,
  at: Readonly<Record<string, bigint>>,
): Room[] {
  const last = (r: Room) => at[r.id] ?? 0n;
  return rooms
    .filter((r) => r.membership !== "invite")
    .sort(
      (a, b) =>
        Number(hasUnread(unread.get(b.id))) - Number(hasUnread(unread.get(a.id))) ||
        (last(a) === last(b) ? 0 : last(a) > last(b) ? -1 : 1) ||
        a.name.localeCompare(b.name) ||
        a.id.localeCompare(b.id),
    );
}

/** total is what the tray counts: the unread of the rooms listed, each once. */
export function total(rooms: readonly Room[], unread: ReadonlyMap<string, Unread>): number {
  return rooms.reduce((sum, r) => sum + unreadOf(unread.get(r.id)).count, 0);
}
