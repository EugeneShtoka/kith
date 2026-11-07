import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { BackendService, Change, RailResponseSchema, RailRowKind, RailRowSchema } from "../gen/backend/v1/services_pb";
import { RoomSchema } from "../gen/backend/v1/types_pb";
import { named, namesRow } from "./rail";
import { peekAfter, Session } from "./session";
import { Kithd, room, settled } from "./testing/kithd";

const backend = BackendService.method;

let kithd: Kithd;
let session: Session;

beforeEach(() => {
  kithd = new Kithd();
  vi.stubGlobal("window", { kith: kithd.bridge() });
  session = new Session();
});

afterEach(() => {
  session.stop();
  vi.unstubAllGlobals();
});

const view = () => session.snapshot();

type Row = [key: string, rooms: string[], space?: string];

/** rail is a rail served at revision, its rows as given (a space's names its space), knowing known. */
function rail(revision: number, rows: Row[], known?: string[]) {
  const ids = known ?? [...new Set(rows.flatMap(([, rooms]) => rooms))];
  return create(RailResponseSchema, {
    revision: BigInt(revision),
    rows: rows.map(([key, rooms, space]) => ({
      key,
      label: key,
      kind: space === undefined ? RailRowKind.TAG : RailRowKind.SPACE,
      space: space ?? "",
      rooms,
    })),
    rooms: ids.map((id) => ({ facts: { id } })),
  } satisfies MessageInitShape<typeof RailResponseSchema>);
}

/** served is kithd serving profile's rail as given, and saying so (CHANGE_RAIL). */
async function served(profile: string, r: ReturnType<typeof rail> | "gone"): Promise<void> {
  kithd.rails.set(profile, r);
  kithd.send(backend.changed, profile, { change: Change.RAIL });
  await settled();
}

const rows = () => view().rail.map((r) => (r.space === "" ? r.key : `${r.key}@${r.space}`));
const at = () => rows()[view().row];
const listed = () => view().rooms.map((r) => r.id);

async function started(rooms: string[], r: ReturnType<typeof rail>): Promise<void> {
  kithd.of("Home").rooms = rooms.map((id) => room(id));
  kithd.rails.set("Home", r);
  session.start();
  await settled();
}

describe("the rail", () => {
  it("lists the rooms of the row the window is on", async () => {
    await started(
      ["!a", "!b", "!c"],
      rail(1, [
        ["DMs", ["!a"]],
        ["Work", ["!b", "!c"]],
      ]),
    );
    expect(rows()).toEqual(["DMs", "Work"]);
    expect(at()).toBe("DMs");
    expect(listed()).toEqual(["!a"]);
    session.selectRow(1);
    expect(listed().sort()).toEqual(["!b", "!c"]);
  });

  it("lists no room until the rail comes, then the row's", async () => {
    kithd.holding.add("Rail");
    await started(["!a", "!b"], rail(1, [["One", ["!a"]]]));
    expect(view().joined.length).toBe(2);
    expect(listed()).toEqual([]);
    kithd.holding.clear();
    kithd.release();
    await settled();
    expect(listed()).toEqual(["!a"]);
  });

  it("shows the latest rail when answers arrive out of order", async () => {
    await started(["!a", "!b"], rail(1, [["One", ["!a"]]]));
    kithd.holding.add("Rail");
    await served("Home", rail(2, [["Two", ["!a"]]]));
    await served("Home", rail(3, [["Three", ["!b"]]]));
    kithd.holding.clear();
    kithd.release(true); // the newer answer first, the older last
    await settled();
    expect(rows()).toEqual(["Three"]);
    expect(listed()).toEqual(["!b"]);
  });

  it("drops a rail read for the profile the window just left", async () => {
    kithd.profiles = ["Home", "Work"];
    kithd.remembered = "Home";
    kithd.of("Work").rooms = [room("!w")];
    kithd.rails.set("Work", rail(1, [["Desk", ["!w"]]]));
    await started(["!a"], rail(1, [["Couch", ["!a"]]]));
    kithd.holding.add("Rail");
    await served("Home", rail(9, [["Couch, changed", ["!a"]]]));
    await session.choose("Work");
    await settled();
    kithd.holding.clear();
    kithd.release(true);
    await settled();
    expect(view().profile).toBe("Work");
    expect(rows()).toEqual(["Desk"]);
    expect(listed()).toEqual(["!w"]);
  });

  it("stays on its row as rows come and go, and on the row in its place when its own goes", async () => {
    await started(
      ["!a", "!b", "!c", "!d"],
      rail(1, [
        ["A", ["!a"]],
        ["B", ["!b"]],
        ["C", ["!c"]],
      ]),
    );
    session.selectRow(1);
    await served(
      "Home",
      rail(2, [
        ["New", ["!d"]],
        ["A", ["!a"]],
        ["B", ["!b"]],
        ["C", ["!c"]],
      ]),
    );
    expect(at()).toBe("B");
    await served(
      "Home",
      rail(3, [
        ["New", ["!d"]],
        ["A", ["!a"]],
        ["C", ["!c"]],
      ]),
    );
    expect(at()).toBe("C"); // B's place
    expect(listed()).toEqual(["!c"]);
    session.selectRow(2);
    await served(
      "Home",
      rail(4, [
        ["New", ["!d"]],
        ["A", ["!a"]],
      ]),
    );
    expect(at()).toBe("A"); // the rail grew shorter: its last row
  });

  it("tells two spaces of one name apart", async () => {
    const both: Row[] = [
      ["Team", ["!a"], "!s1"],
      ["Team", ["!b"], "!s2"],
    ];
    await started(["!a", "!b"], rail(1, both));
    session.selectRow(1);
    await served("Home", rail(2, [["New", []], ...both]));
    expect(at()).toBe("Team@!s2");
    await served("Home", rail(3, [both[1] as Row, both[0] as Row]));
    expect(at()).toBe("Team@!s2");
    expect(listed()).toEqual(["!b"]);
  });

  it("keeps an open room the rail does not know listed in the row the window is on", async () => {
    await started(
      ["!a", "!new"],
      rail(
        1,
        [
          ["A", ["!a"]],
          ["B", []],
        ],
        ["!a"],
      ),
    );
    session.open("!new");
    await settled();
    expect(listed().sort()).toEqual(["!a", "!new"]);
    session.selectRow(1);
    expect(listed()).toEqual(["!new"]);
    // Known, and held by another row, it is that row's.
    await served(
      "Home",
      rail(2, [
        ["A", ["!a", "!new"]],
        ["B", []],
      ]),
    );
    expect(listed()).toEqual([]);
  });

  it("shows the room the place moved to lists, once the rail rests: the open one where it is listed, else the first, else none", async () => {
    const wait = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));
    const rested = () => wait(peekAfter + 20);
    await started(
      ["!a", "!b", "!c"],
      rail(1, [
        ["A", ["!a"]],
        ["B", ["!b", "!c"]],
        ["C", ["!a", "!c"]],
        ["Empty", []],
      ]),
    );
    session.open("!a");
    await settled();
    session.selectRow(1);
    expect(view().open).toBe("!a"); // not while the rail moves
    await rested();
    expect([view().open, listed().includes(view().open)]).toEqual([listed()[0], true]);
    session.open("!c");
    await settled();
    session.selectRow(2); // it lists the open room: it stays
    await rested();
    expect(view().open).toBe("!c");
    session.selectRow(1); // moving on before the rail rests starts the wait again
    await wait(peekAfter / 2);
    session.selectRow(0);
    await wait(peekAfter / 2 + 20);
    expect(view().open).toBe("!c");
    session.selectRow(0); // a fast run along the rail shows only where it rests
    session.selectRow(1);
    session.selectRow(3);
    await rested();
    expect([view().open, view().timeline, view().composer]).toEqual(["", undefined, undefined]);
    session.back(); // a room shown only as the rail passed is not one the window went to
    await settled();
    expect(view().open).toBe("!a");
  });

  it("keeps the open room listed in a sticky tag's row it left, until another room is open", async () => {
    const unread = (revision: number, rooms: string[], sticky: boolean) => {
      const r = rail(revision, [["Unread", rooms]], ["!a", "!b"]);
      for (const row of r.rows) {
        row.sticky = sticky;
      }
      return r;
    };
    await started(["!a", "!b"], unread(1, ["!a", "!b"], true));
    session.open("!a");
    await served("Home", unread(2, ["!b"], true)); // read, !a leaves Unread
    expect(listed().sort()).toEqual(["!a", "!b"]);
    session.peek("!b");
    expect(listed()).toEqual(["!b"]);
    session.open("!a");
    await served("Home", unread(3, ["!b"], false)); // the same row, not sticky
    expect(listed()).toEqual(["!b"]);
  });

  it("empties with a profile gone from the config, and the window asks for one", async () => {
    kithd.profiles = ["Home", "Work"];
    kithd.remembered = "Home";
    await started(["!a"], rail(1, [["A", ["!a"]]]));
    kithd.profiles = ["Work", "Play"];
    await served("Home", "gone");
    expect(view().profile).toBe("");
    expect(view().rail).toEqual([]);
    expect(view().rooms).toEqual([]);
  });
});

describe("a home entry", () => {
  const space = create(RailRowSchema, { key: "Work", label: "Office", kind: RailRowKind.SPACE, space: "!w:x" });
  const tag = create(RailRowSchema, { key: "tag:Later", label: "Later", kind: RailRowKind.TAG });
  for (const [entry, row, want] of [
    ["space:work", space, true], // case ignored
    ["space: Work ", space, true],
    ["tag:Work", space, false], // a tag is not a space
    ["place:Work", space, true],
    ["place:office", space, true], // by its label
    ["space:Office", space, false], // space: names the space, not its label
    ["tag:later", tag, true],
    ["space:Later", tag, false],
    ["place:Later", tag, true],
    ["place:", tag, false], // no name
    ["Later", tag, false], // no prefix
  ] as const) {
    it(`${entry} names ${row.key}: ${String(want)}`, () => {
      expect(namesRow(entry, row)).toBe(want);
    });
  }
});

describe("the names you gave rooms", () => {
  // A room the rail names takes that name; one it serves with none, one it does not
  // serve, and a rail not read yet leave the room's own.
  it("show in place of the rooms' own, where the rail names them", () => {
    const rooms = ["!a", "!b", "!c"].map((id) => create(RoomSchema, { id, name: id.slice(1).toUpperCase() }));
    const served = create(RailResponseSchema, {
      rooms: [
        { facts: { id: "!a" }, name: "Boss" },
        { facts: { id: "!b" }, name: "" },
        { facts: { id: "!z" }, name: "Gone" },
      ],
    });
    expect(named(rooms, served).map((r) => r.name)).toEqual(["Boss", "B", "C"]);
    expect(named(rooms, undefined).map((r) => r.name)).toEqual(["A", "B", "C"]);
  });
});
