import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { BackendService, Change, RailResponseSchema, RailRowKind } from "../gen/backend/v1/services_pb";
import { Session } from "./session";
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
