import { create } from "@bufbuild/protobuf";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { BackendService, Change, type SeatRequest } from "../gen/backend/v1/services_pb";
import { ReactionSchema } from "../gen/backend/v1/types_pb";
import { DeviceSettingsResponseSchema } from "../gen/device/v1/device_pb";
import { unreadOf } from "./rooms";
import { Session } from "./session";
import { Kithd, room, settled, unread } from "./testing/kithd";
import { message } from "./testing/rows";

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

async function started(): Promise<void> {
  session.start();
  await settled();
}

const view = () => session.snapshot();

/** The streams a seated window holds in its profile, besides its seat's. */
const profileStreams = [
  backend.changed,
  backend.configChanged,
  backend.messages,
  backend.reactions,
  backend.unreadStream,
  backend.invites,
].map((m) => m.name);

describe("the window's profile", () => {
  it("is the only one there is, unasked and not written down", async () => {
    kithd.profiles = ["Solo"];
    await started();
    expect(view().profile).toBe("Solo");
    expect(view().seat.state).toBe("seated");
    expect(kithd.remembered).toBe("");
    expect(kithd.calls).not.toContain("ChooseProfile:");
  });

  it("is asked for on a first connect with several, and nothing reaches kithd until it is chosen", async () => {
    kithd.profiles = ["Home", "Work"];
    await started();
    expect(view().profile).toBe("");
    expect(view().profiles).toEqual(["Home", "Work"]);
    expect(view().seat.state).toBe("none");
    expect(kithd.calls).toEqual(["Device:"]);
    expect(kithd.refused).toEqual([]);

    await session.choose("Work");
    await settled();
    expect(view().profile).toBe("Work");
    expect(view().seat.state).toBe("seated");
    expect(kithd.remembered).toBe("Work");
    expect(kithd.refused).toEqual([]);
    for (const c of kithd.calls.slice(2)) {
      expect(c).toMatch(/:Work$/);
    }
    expect(kithd.open().map((s) => s.profile)).toEqual(Array(7).fill("Work"));
  });

  it("is the one remembered, when the config still has it", async () => {
    kithd.profiles = ["Home", "Work"];
    kithd.remembered = "Work";
    await started();
    expect(view().profile).toBe("Work");
    expect(kithd.calls).not.toContain("ChooseProfile:");
  });

  it("switches with everything it shows, and the old profile's streams close", async () => {
    kithd.profiles = ["Home", "Work"];
    kithd.remembered = "Home";
    kithd.of("Home").rooms = [room("!h")];
    kithd.of("Home").unread.set("!h", unread("!h", 2));
    kithd.of("Work").rooms = [room("!w")];
    await started();
    expect(view().rooms.map((r) => r.id)).toEqual(["!h"]);
    expect(view().unreadTotal).toBe(2);

    await session.choose("Work");
    await settled();
    expect(view().rooms.map((r) => r.id)).toEqual(["!w"]);
    expect(view().unreadTotal).toBe(0);
    expect(kithd.told.at(-1)).toBe(0);
    expect(kithd.open().every((s) => s.profile === "Work")).toBe(true);
    expect(kithd.seats.has("Home")).toBe(false);
  });

  it("removed by a config reload: with several left the list empties and the window asks; with one left it runs there", async () => {
    kithd.profiles = ["Home", "Work"];
    kithd.remembered = "Work";
    kithd.of("Work").rooms = [room("!w")];
    kithd.of("Work").unread.set("!w", unread("!w", 4));
    await started();
    expect(view().unreadTotal).toBe(4);

    kithd.profiles = ["Home", "Play"];
    kithd.send(backend.configChanged, "Work", { config: "" });
    await settled();
    expect(view().profile).toBe("");
    expect(view().profiles).toEqual(["Home", "Play"]);
    expect(view().rooms).toEqual([]);
    expect(view().seat.state).toBe("none");
    expect(kithd.told.at(-1)).toBe(0);
    expect(kithd.open()).toEqual([]);
    expect(kithd.refused).toEqual([]);

    await session.choose("Play");
    await settled();
    kithd.profiles = ["Solo"];
    kithd.send(backend.configChanged, "Play", { config: "" });
    await settled();
    expect(view().profile).toBe("Solo");
    expect(view().seat.state).toBe("seated");
  });

  it("removed while kithd was away: the window asks once kithd is back", async () => {
    kithd.profiles = ["Home", "Work"];
    kithd.remembered = "Work";
    await started();
    kithd.down();
    kithd.profiles = ["Home", "Play"];
    kithd.again();
    await settled();
    expect(view().profile).toBe("");
    expect(kithd.open()).toEqual([]);
  });
});

describe("the window's seat", () => {
  it("taken elsewhere: nothing of the profile is read until it is taken over, by force", async () => {
    kithd.of("Home").rooms = [room("!a")];
    kithd.seats.set("Home", "other");
    await started();
    expect(view().seat).toMatchObject({ state: "taken", holder: { pid: 4312n, host: "elsewhere" } });
    expect(kithd.open()).toEqual([]);
    expect(kithd.calls.filter((c) => !c.startsWith("Device"))).toEqual([]);

    session.takeOver();
    await settled();
    expect(view().seat.state).toBe("seated");
    const seats = kithd.subs.filter((s) => s.method === backend.seat.name);
    expect(seats.map((s) => (s.request as SeatRequest).force)).toEqual([false, true]);
    expect(kithd.open().map((s) => s.method)).toEqual([backend.seat.name, ...profileStreams]);
    expect(view().rooms.map((r) => r.id)).toEqual(["!a"]);
  });

  it("taken over by another window: every stream stops, the rooms and the badge go, and a late answer brings nothing back", async () => {
    kithd.of("Home").rooms = [room("!a"), room("!b")];
    kithd.of("Home").unread.set("!a", unread("!a", 3));
    kithd.of("Home").invites = [room("!i", { membership: "invite" })];
    await started();
    expect(view().unreadTotal).toBe(3);
    expect(view().invites).toHaveLength(1);

    // kithd restarts and the window reads again, its answers still on their way.
    kithd.holding.add("CachedUnread").add("CachedInvites").add("Rooms");
    kithd.down();
    kithd.again();
    await settled();
    kithd.otherTakesOver("Home");
    await settled();
    kithd.release();
    kithd.send(backend.unreadStream, "Home", { unread: unread("!b", 9) });
    await settled();

    expect(view().seat.state).toBe("steppedAside");
    expect(view().rooms).toEqual([]);
    expect(view().invites).toEqual([]);
    expect(view().unread.size).toBe(0);
    expect(view().unreadTotal).toBe(0);
    expect(kithd.told.at(-1)).toBe(0);
    expect(kithd.open()).toEqual([]);
    expect(kithd.seats.get("Home")).toBe("other");

    kithd.holding.clear();
    session.takeOver();
    await settled();
    expect(view().seat.state).toBe("seated");
    expect(kithd.seats.get("Home")).not.toBe("other");
    expect(view().unreadTotal).toBe(3);
  });

  it("after kithd restarts is asked for again, unforced: a window that took it meanwhile keeps it", async () => {
    session.takeOver(); // no profile yet: nothing
    await started();
    kithd.down();
    kithd.seats.set("Home", "other");
    kithd.again();
    await settled();
    expect(view().seat.state).toBe("taken");
    expect(kithd.seats.get("Home")).toBe("other");
    expect(kithd.open()).toEqual([]);
  });
});

describe("the window's invitations", () => {
  it("are accepted by joining and declined by leaving", async () => {
    kithd.of("Home").invites = [room("!i", { membership: "invite" }), room("!j", { membership: "invite" })];
    await started();
    await session.accept("!i");
    await session.decline("!j");
    expect(kithd.calls.filter((c) => /^(JoinRoom|LeaveRoom)/.test(c))).toEqual(["JoinRoom:Home", "LeaveRoom:Home"]);
    expect(kithd.calls.at(-1)).toBe("LeaveRoom:Home");
  });
});

/** A small seeded generator, so a failure names the seed that made it. */
function rng(seed: number) {
  let s = seed;
  return (n: number) => {
    s = (s * 1103515245 + 12345) % 2 ** 31;
    return Math.floor((s / 2 ** 31) * n); // the high bits: an LCG's low ones repeat soon
  };
}

describe("the rooms, the unread and the invitations", () => {
  // kithd restarts at any point; the rooms, counts and invitations change while it is
  // away and while the window's reads of them are on their way, answered in or out of
  // order; frames race those reads. Whenever all is quiet, the window shows what kithd
  // holds, each room counted once, and has told the tray so.
  it.each(Array.from({ length: 40 }, (_, i) => i + 1))("hold over kithd restarts (seed %i)", async (seed) => {
    const pick = rng(seed);
    const all = ["!r0", "!r1", "!r2", "!r3"];
    const want = kithd.of("Home");
    want.rooms = all.map((id) => room(id));
    await started();
    const check = (when: string) => {
      const ids = want.rooms.map((r) => r.id);
      expect(
        view()
          .rooms.map((r) => r.id)
          .sort(),
        when,
      ).toEqual([...ids].sort());
      for (const id of ids) {
        expect(unreadOf(view().unread.get(id)).count, `${id}, ${when}`).toBe(unreadOf(want.unread.get(id)).count);
      }
      const total = ids.reduce((sum, id) => sum + unreadOf(want.unread.get(id)).count, 0);
      expect(view().unreadTotal, when).toBe(total);
      expect(kithd.told.at(-1) ?? 0, when).toBe(total);
      expect(
        view().invites.map((r) => r.id),
        when,
      ).toEqual(want.invites.map((r) => r.id));
    };
    const reads = ["CachedUnread", "CachedInvites", "Rooms"];
    const holdSome = () => {
      for (const read of reads) {
        if (pick(2) === 0) {
          kithd.holding.add(read);
        }
      }
    };
    let up = true;
    for (let step = 0; step < 120; step++) {
      // A few turns of the event loop between steps: what was sent arrives, or not yet.
      for (let i = pick(3); i > 0; i--) {
        await new Promise((resolve) => setTimeout(resolve, 0));
      }
      switch (pick(12)) {
        case 0:
        case 1:
        case 2:
        case 3:
          kithd.setUnread("Home", unread(all[pick(all.length)] ?? "", pick(4)));
          break;
        case 4:
          kithd.setInvites(
            "Home",
            ["!i0", "!i1", "!i2"].filter(() => pick(2) === 0).map((id) => room(id, { membership: "invite" })),
          );
          break;
        case 5:
        case 6:
          want.rooms = all.filter(() => pick(3) > 0).map((id) => room(id));
          if (up) {
            kithd.send(backend.changed, "Home", { change: Change.ROOMS });
            kithd.send(backend.changed, "Home", { change: Change.RAIL }); // its rail holds them
          }
          break;
        case 7:
        case 8:
          // Back up, the window reads everything again: some of those reads are slow.
          if (up) {
            kithd.down();
          } else {
            holdSome();
            kithd.again();
          }
          up = !up;
          break;
        case 9:
          holdSome();
          break;
        case 10:
          break;
        default:
          kithd.holding.clear();
          kithd.release(pick(2) === 0);
          if (up) {
            await settled();
            check(`seed ${String(seed)}, step ${String(step)}`);
          }
      }
    }
    kithd.holding.clear();
    if (!up) {
      kithd.again();
    }
    await settled();
    kithd.release(true);
    await settled();
    check(`seed ${String(seed)}, at the end`);
  });
});

describe("the open room", () => {
  const r = "!r";
  const ids = () => view().timeline?.rows.map((m) => m.id) ?? [];
  const seed = (n: number, sender = "@alice") => {
    for (let i = 1; i <= n; i++) {
      kithd.write("Home", message({ id: `$${String(i)}`, ts: i * 10, body: `m${String(i)}`, sender }, r));
    }
  };

  beforeEach(() => {
    kithd.of("Home").rooms = [room(r), room("!other")];
  });

  it("shows the room's newest messages, then older ones as it scrolls back, kept by the stream", async () => {
    seed(6);
    kithd.of("Home").unread.set(r, { ...unread(r, 2), readEvent: "$4" });
    await started();
    session.open(r);
    await settled();
    expect(ids()).toEqual(["$3", "$4", "$5", "$6"]);
    expect(view().timeline?.unreadFrom).toBe("$4");

    kithd.write("Home", message({ id: "$5", ts: 50, body: "edited", edited: true, editedAt: 900 }, r));
    kithd.write("Home", message({ id: "$7", ts: 70 }, r));
    kithd.write("Home", message({ id: "$x", ts: 75 }, "!other"));
    kithd.write("Home", message({ id: "$1", ts: 10, redacted: true }, r)); // outside what is shown
    await settled();
    expect(ids()).toEqual(["$3", "$4", "$5", "$6", "$7"]);
    expect(view().timeline?.rows.find((m) => m.id === "$5")?.body).toBe("edited");

    session.older();
    await settled();
    expect(ids()).toEqual(["$1", "$2", "$3", "$4", "$5", "$6", "$7"]);
    expect(view().timeline?.rows[0]?.redacted).toBe(true);
    session.older();
    await settled();
    expect(view().timeline?.start).toBe(true);
    expect(view().problem).toBeUndefined(); // the network being off is not one
  });

  it("reads nothing of it before the Messages stream says where it starts", async () => {
    seed(2);
    kithd.down();
    await started();
    session.open(r);
    await settled();
    expect(kithd.calls.filter((c) => c.startsWith("CachedTimeline"))).toEqual([]);
    kithd.again();
    await settled();
    expect(ids()).toEqual(["$1", "$2"]);
  });

  it("is not read again when kithd comes back: main resumes the stream where it left off", async () => {
    seed(3);
    await started();
    session.open(r);
    await settled();
    kithd.down();
    kithd.again();
    await settled();
    expect(kithd.calls.filter((c) => c.startsWith("CachedTimeline"))).toHaveLength(1);
  });

  it("is read again, with the rooms, when kithd could not resume", async () => {
    seed(3);
    await started();
    session.open(r);
    await settled();
    // A change the stream lost: the cache holds it, no frame says so.
    kithd.down();
    kithd
      .of("Home")
      .messages.set(r, [
        ...(kithd.of("Home").messages.get(r) ?? []).slice(0, 2),
        message({ id: "$3", ts: 30, redacted: true }, r),
      ]);
    kithd.again();
    kithd.readAgain("Home");
    await settled();
    expect(view().timeline?.rows.at(-1)?.redacted).toBe(true);
    expect(kithd.calls.filter((c) => c.startsWith("CachedTimeline"))).toHaveLength(2);
    expect(kithd.calls.filter((c) => c.startsWith("Rooms")).length).toBeGreaterThan(1);
  });

  it("has its reactions read as it opens, and a reaction taken back while they are read stays gone", async () => {
    seed(1);
    const reaction = create(ReactionSchema, { id: "$re", roomId: r, target: "$1", key: "👍" });
    kithd.of("Home").reactions = [reaction];
    await started();
    kithd.holding.add("CachedReactions");
    session.open(r);
    await settled();
    kithd.send(backend.reactions, "Home", { update: { reaction, removed: true } });
    kithd.send(backend.reactions, "Home", { update: { reaction: { ...reaction, id: "$re2", key: "🎉" } } });
    await settled();
    kithd.release();
    await settled();
    expect(
      view()
        .timeline?.reactions.get("$1")
        ?.map((x) => x.key),
    ).toEqual(["🎉"]);
  });

  it("fetches a reply's target that is not shown", async () => {
    seed(6);
    kithd.write("Home", message({ id: "$9", ts: 90, replyTo: "$1" }, r));
    await started();
    session.open(r);
    await settled();
    expect(view().timeline?.quotes.get("$1")?.body).toBe("m1");
  });
});

describe("read receipts", () => {
  const r = "!r";
  const open = async () => {
    kithd.of("Home").rooms = [room(r)];
    kithd.write("Home", message({ id: "$1", ts: 10, sender: "@alice" }, r));
    await started();
    session.open(r);
    await settled();
  };

  it("are sent while the room is watched, once a message, for its newest", async () => {
    await open();
    expect(kithd.receipts).toEqual([]); // not watched
    session.watch(true);
    await settled();
    kithd.write("Home", message({ id: "$2", ts: 20, sender: "@alice" }, r));
    kithd.write("Home", message({ id: "$t", ts: 30, sender: "@alice", threadRoot: "$1" }, r));
    await settled();
    session.watch(false);
    kithd.write("Home", message({ id: "$3", ts: 40, sender: "@alice" }, r));
    await settled();
    session.watch(true);
    session.watch(true);
    await settled();
    expect(kithd.receipts).toEqual([`${r} $1 public`, `${r} $2 public`, `${r} $3 public`]);
  });

  it("are private where send_receipts or a rule says so", async () => {
    kithd.settings = create(DeviceSettingsResponseSchema, {
      sendReceipts: true,
      unreadLine: true,
      readRules: [{ match: "", sender: "@alice", send: false }],
    });
    await open();
    session.watch(true);
    await settled();
    expect(kithd.receipts).toEqual([`${r} $1 private`]);
  });

  it("are private when a rule naming a home might name this room: the window does not know its homes", async () => {
    kithd.settings = create(DeviceSettingsResponseSchema, {
      sendReceipts: true,
      readRules: [{ match: "space:Work", send: false }],
    });
    await open();
    session.watch(true);
    await settled();
    expect(kithd.receipts).toEqual([`${r} $1 private`]);
    expect(view().unreadLine).toBe(false);
  });

  it("are private when the device settings cannot be read", async () => {
    kithd.settings = "fail";
    await open();
    session.watch(true);
    await settled();
    expect(kithd.receipts).toEqual([`${r} $1 private`]);
    expect(view().problem).toMatch(/does not load/);
  });

  it("wait for the settings: none is sent before they are read", async () => {
    kithd.holding.add("DeviceSettings");
    await open();
    session.watch(true);
    await settled();
    expect(kithd.receipts).toEqual([]);
    kithd.release();
    await settled();
    expect(kithd.receipts).toEqual([`${r} $1 public`]);
  });
});

describe("an attachment", () => {
  it("is saved through main, by its room, event and name", async () => {
    kithd.of("Home").rooms = [room("!r")];
    await started();
    await session.save(message({ id: "$1", media: "cat.png" }, "!r"));
    expect(kithd.saved).toEqual(["!r $1 cat.png"]);
  });
});
