// The room list's thread rows ([display.threads]), as the TUI lists them (roomRows): under
// each room its threads with unread replies, newest first, then the thread open and the one
// the cursor is on, which keep their row once read; at most a cap of them, the rest counted
// into one row. The cap is domain.ThreadCap, held to Go by the conformance table
// internal/domain/testdata/threadcap.json. A folded room ([display.threads] folded) lists
// none; the room whose overflow row was entered lists them all.
import { create } from "@bufbuild/protobuf";

import type { RailResponse } from "../gen/backend/v1/services_pb";
import {
  type Message,
  type Room,
  type ThreadUnread,
  ThreadUnreadSchema,
  type Unread,
} from "../gen/backend/v1/types_pb";
import type { DeviceSettings } from "../gen/device/v1/device_pb";
import { namesHome } from "./rail";
import { collapse } from "./threads";

/**
 * One row of the room list: a room, one of its threads, or the count of its threads not listed.
 * A room's threads are how many it lists under it, or would unfolded.
 */
export type ListRow =
  | { kind: "room"; room: Room; silenced: boolean; threads: number; folded: boolean }
  | { kind: "thread"; room: Room; thread: ThreadUnread }
  | { kind: "more"; room: Room; more: number };

/** What the room list's thread rows follow of the device's settings. */
export type ThreadList = Pick<
  DeviceSettings,
  "threadsInRoomList" | "threadsMax" | "threadsDefaultMax" | "threadsMarker" | "threadRules" | "threadsFolded"
>;

// The rungs a rule names a room on (domain.Rank): its first home, or itself.
const rankHome = 1;
const rankRoom = 3;

/**
 * threadCap is how many of room's threads are listed under it, its first home home
 * (labeled label on the rail): the narrowest rule naming it, the later on a tie, else
 * threadsMax; a rule's or max's 0 is the default, and 0 back is no cap (domain.ThreadCap).
 */
export function threadCap(list: ThreadList, room: string, home: string, label: string): number {
  let base = Number(list.threadsMax);
  let best = 0;
  for (const rule of list.threadRules) {
    const place = rule.match.trim();
    const rank =
      place === "" ? 0 : place === room ? rankRoom : home !== "" && namesHome(place, home, label) ? rankHome : 0;
    if (rank > 0 && rank >= best) {
      best = rank;
      base = Number(rule.max);
    }
  }
  return base < 0 ? 0 : base === 0 ? Number(list.threadsDefaultMax) : base;
}

/**
 * pinnedThread is root's row from the open room's timeline rows, read: its newest reply,
 * and its root's text. Undefined while the timeline holds none of it.
 */
export function pinnedThread(rows: readonly Message[], root: string): ThreadUnread | undefined {
  const t = collapse(rows).threads.find((s) => s.root === root);
  if (t === undefined) {
    return undefined;
  }
  return create(ThreadUnreadSchema, {
    root,
    latest: t.latest,
    latestAt: rows.find((m) => m.id === t.latest)?.timestamp,
    title: rows.find((m) => m.id === root)?.body ?? "",
  });
}

/**
 * listRows is the room list as drawn: each of rooms, marked when a silent tag holds it
 * (Archived), and under it its threads, unless threads are never listed, the settings are
 * not read yet, a silent tag holds it, or it is folded.
 * pinned are the open room's threads kept listed once read, after its unread ones;
 * expanded is the room listing them all, past the cap.
 */
export function listRows(
  rooms: readonly Room[],
  unread: ReadonlyMap<string, Unread>,
  rail: RailResponse | undefined,
  list: ThreadList | undefined,
  open: string,
  pinned: readonly ThreadUnread[],
  expanded = "",
): ListRow[] {
  const served = new Map((rail?.rooms ?? []).map((r) => [r.facts?.id ?? "", r]));
  const rows: ListRow[] = [];
  for (const room of rooms) {
    const judged = served.get(room.id);
    const silenced = judged?.silenced === true;
    const row = { kind: "room" as const, room, silenced, threads: 0, folded: false };
    rows.push(row);
    if (list === undefined || list.threadsInRoomList === "never" || silenced) {
      continue;
    }
    const threads = [...(unread.get(room.id)?.threads ?? [])];
    if (room.id === open) {
      threads.push(...pinned.filter((p) => !threads.some((t) => t.root === p.root)));
    }
    row.threads = threads.length;
    row.folded = list.threadsFolded.includes(room.id);
    if (row.folded) {
      continue;
    }
    const cap = room.id === expanded ? 0 : threadCap(list, room.id, judged?.home ?? "", judged?.homeLabel ?? "");
    const shown = cap > 0 ? threads.slice(0, cap) : threads;
    rows.push(...shown.map((thread) => ({ kind: "thread" as const, room, thread })));
    if (shown.length < threads.length) {
      rows.push({ kind: "more", room, more: threads.length - shown.length });
    }
  }
  return rows;
}
