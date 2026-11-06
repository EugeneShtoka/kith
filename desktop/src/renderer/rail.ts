// The rail is kithd's (Rail): the rows it built for the window's profile, each with its
// rooms, and every room it judged. The window draws the rows, keeps its place among them
// as they change, and lists the rooms of the row it is on.
import type { RailResponse, RailRow } from "../gen/backend/v1/services_pb";
import type { Room, Unread } from "../gen/backend/v1/types_pb";
import { unreadOf } from "./rooms";

/** A row across rails: its key, and for a space's row the space (two can share a name). */
export interface RowRef {
  key: string;
  space: string;
}

/**
 * namesRow: a home entry (space:, tag:, place:) names row, as domain.NamesHome holds:
 * space: a space's name, tag: a tag's, place: either, or the row's label; case ignored.
 */
export function namesRow(entry: string, row: RailRow): boolean {
  const m = /^\s*(space|tag|place):(.*)$/i.exec(entry);
  const want = m?.[2]?.trim().toLowerCase() ?? "";
  if (m === null || want === "") {
    return false;
  }
  const tag = /^\s*tag:(.*)$/i.exec(row.key);
  const name = (tag?.[1] ?? row.key).trim().toLowerCase();
  switch (m[1]?.toLowerCase()) {
    case "space":
      return tag === null && name === want;
    case "tag":
      return tag !== null && name === want;
  }
  return name === want || row.label.trim().toLowerCase() === want;
}

export function refOf(row: RailRow | undefined): RowRef | undefined {
  return row === undefined ? undefined : { key: row.key, space: row.space };
}

/**
 * named is rooms under the names you gave them ([[display.name]]), as the rail serves
 * them, in place of their own; rooms the rail names none of are as they were.
 */
export function named(rooms: readonly Room[], rail: RailResponse | undefined): readonly Room[] {
  const names = new Map((rail?.rooms ?? []).filter((r) => r.name !== "").map((r) => [r.facts?.id ?? "", r.name]));
  return rooms.map((r) => {
    const name = names.get(r.id);
    return name === undefined ? r : { ...r, name };
  });
}

/** later: got may replace held, a rail served no earlier (an answer overtaken arrives late). */
export function later(held: RailResponse | undefined, got: RailResponse): boolean {
  return held === undefined || got.revision >= held.revision;
}

/**
 * placed is where the window is on rows: the row it was on, found by key and space, or,
 * gone, the row now in its place (at), the last when the rail grew shorter.
 */
export function placed(rows: readonly RailRow[], was: RowRef | undefined, at: number): number {
  const same = rows.findIndex((r) => was !== undefined && r.key === was.key && r.space === was.space);
  if (same >= 0) {
    return same;
  }
  return rows.length === 0 ? -1 : Math.min(Math.max(at, 0), rows.length - 1);
}

/**
 * listed is rooms as the row lists them: those it holds, and the room open when the rail
 * does not know it (a chat newer than the rail), so it stays where the person is. With
 * no rail yet, every room.
 */
export function listed(rooms: readonly Room[], rail: RailResponse | undefined, row: number, open: string): Room[] {
  if (rail === undefined) {
    return [...rooms];
  }
  const holds = new Set(rail.rows[row]?.rooms ?? []);
  const known = rail.rooms.some((r) => r.facts?.id === open);
  return rooms.filter((r) => holds.has(r.id) || (r.id === open && !known));
}

/** rowUnread is what a row's badge counts: its rooms' unread, and whether any mentions. */
export function rowUnread(row: RailRow, unread: ReadonlyMap<string, Unread>): { count: number; mentions: number } {
  let count = 0;
  let mentions = 0;
  for (const id of new Set(row.rooms)) {
    const u = unreadOf(unread.get(id));
    count += u.count;
    mentions += u.mentions;
  }
  return { count, mentions };
}
