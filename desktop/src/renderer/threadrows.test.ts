import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import table from "../../../internal/domain/testdata/threadcap.json";
import { type RailResponse, RailResponseSchema } from "../gen/backend/v1/services_pb";
import { type ThreadUnread, ThreadUnreadSchema, type Unread, UnreadSchema } from "../gen/backend/v1/types_pb";
import { DeviceSettingsSchema } from "../gen/device/v1/device_pb";
import { room } from "./testing/kithd";
import { message } from "./testing/rows";
import { type ListRow, listRows, pinnedThread, threadCap, type ThreadList } from "./threadrows";

interface Case {
  name: string;
  max: number;
  fallback: number;
  rules: { match: string; max: number }[];
  room: string;
  home: string;
  homeLabel?: string;
  want: number;
}

const settings = (init: Partial<Record<"threadsInRoomList" | "threadsMarker", string>> & { max?: number } = {}) =>
  create(DeviceSettingsSchema, {
    threadsInRoomList: init.threadsInRoomList ?? "unread",
    threadsMax: BigInt(init.max ?? 0),
    threadsDefaultMax: 5n,
    threadsMarker: init.threadsMarker ?? "↳",
  });

// The same table internal/domain's conformance test holds to ThreadCap.
describe("how many of a room's threads are listed, as Go resolves it", () => {
  const cases = table.cases as Case[];
  it("has cases", () => {
    expect(cases.length).toBeGreaterThan(0);
  });
  for (const c of cases) {
    it(c.name, () => {
      const list: ThreadList = create(DeviceSettingsSchema, {
        threadsMax: BigInt(c.max),
        threadsDefaultMax: BigInt(c.fallback),
        threadRules: c.rules.map((r) => ({ match: r.match, max: BigInt(r.max) })),
      });
      expect(threadCap(list, c.room, c.home, c.homeLabel ?? "")).toBe(c.want);
    });
  }
});

const thread = (root: string, unread = 1): ThreadUnread =>
  create(ThreadUnreadSchema, { root, unread: BigInt(unread), latest: `${root}-reply`, title: `about ${root}` });

const unreadOf = (roomId: string, ...threads: ThreadUnread[]): [string, Unread] => [
  roomId,
  create(UnreadSchema, { roomId, messages: 1n, counted: true, threads }),
];

/** drawn is rows as "room", "room ↳ root" and "room +N". */
const drawn = (rows: readonly ListRow[]) =>
  rows.map((r) =>
    r.kind === "room"
      ? r.room.id
      : r.kind === "thread"
        ? `${r.room.id} ↳ ${r.thread.root}`
        : `${r.room.id} +${String(r.more)}`,
  );

describe("the room list's thread rows", () => {
  const rooms = [room("!a"), room("!b")];

  it("list a room's unread threads under it, in the order kithd gives them", () => {
    const unread = new Map([unreadOf("!a", thread("$2"), thread("$1"))]);
    expect(drawn(listRows(rooms, unread, undefined, settings(), "", []))).toEqual(["!a", "!a ↳ $2", "!a ↳ $1", "!b"]);
  });

  it("are none before the settings are read, where in_room_list is never, or under a room a silent tag holds", () => {
    const unread = new Map([unreadOf("!a", thread("$1")), unreadOf("!b", thread("$2"))]);
    expect(drawn(listRows(rooms, unread, undefined, undefined, "", []))).toEqual(["!a", "!b"]);
    expect(drawn(listRows(rooms, unread, undefined, settings({ threadsInRoomList: "never" }), "", []))).toEqual([
      "!a",
      "!b",
    ]);
    const rail = create(RailResponseSchema, { rooms: [{ facts: { id: "!b" }, silenced: true }] });
    expect(drawn(listRows(rooms, unread, rail, settings(), "", []))).toEqual(["!a", "!a ↳ $1", "!b"]);
  });

  it("mark a room a silent tag holds (Archived), so its count is drawn without a pill, as the terminal's ·N", () => {
    const rail = create(RailResponseSchema, { rooms: [{ facts: { id: "!b" }, silenced: true }] });
    const silenced = (rail: RailResponse | undefined) =>
      listRows(rooms, new Map(), rail, settings(), "", []).flatMap((r) =>
        r.kind === "room" && r.silenced ? [r.room.id] : [],
      );
    expect(silenced(rail)).toEqual(["!b"]);
    expect(silenced(undefined)).toEqual([]);
  });

  it("stop at the cap, the rest counted in one row; a rule naming the room's first home caps it otherwise", () => {
    const unread = new Map([
      unreadOf("!a", thread("$1"), thread("$2"), thread("$3")),
      unreadOf("!b", thread("$4"), thread("$5"), thread("$6")),
    ]);
    const list = create(DeviceSettingsSchema, {
      threadsInRoomList: "unread",
      threadsMax: 2n,
      threadsDefaultMax: 5n,
      threadRules: [{ match: "place:Office", max: -1n }],
    });
    const rail = create(RailResponseSchema, { rooms: [{ facts: { id: "!b" }, home: "Work", homeLabel: "Office" }] });
    expect(drawn(listRows(rooms, unread, rail, list, "", []))).toEqual([
      "!a",
      "!a ↳ $1",
      "!a ↳ $2",
      "!a +1",
      "!b",
      "!b ↳ $4",
      "!b ↳ $5",
      "!b ↳ $6",
    ]);
  });

  it("keep the open room's threads read since after its unread ones, each once; another room's are not kept", () => {
    const unread = new Map([unreadOf("!a", thread("$1"))]);
    const pinned = [thread("$1", 0), thread("$9", 0)];
    expect(drawn(listRows(rooms, unread, undefined, settings(), "!a", pinned))).toEqual([
      "!a",
      "!a ↳ $1",
      "!a ↳ $9",
      "!b",
    ]);
    expect(drawn(listRows(rooms, unread, undefined, settings(), "!b", pinned))).toEqual([
      "!a",
      "!a ↳ $1",
      "!b",
      "!b ↳ $1",
      "!b ↳ $9",
    ]);
  });
});

describe("a read thread's row, from the open room's timeline", () => {
  const rows = [
    message({ id: "$root", ts: 10, sender: "@alice", body: "Lunch?" }),
    message({ id: "$r1", ts: 20, sender: "@bob", threadRoot: "$root" }),
    message({ id: "$r2", ts: 30, sender: "@carol", threadRoot: "$root" }),
    message({ id: "$x", ts: 40, sender: "@alice" }),
  ];

  it("is named by its root's text and points at its newest reply", () => {
    expect(pinnedThread(rows, "$root")).toMatchObject({ root: "$root", title: "Lunch?", latest: "$r2", unread: 0n });
  });

  it("is none for a thread the timeline holds nothing of", () => {
    expect(pinnedThread(rows, "$x")).toBeUndefined();
    expect(pinnedThread(rows, "$gone")).toBeUndefined();
  });
});
