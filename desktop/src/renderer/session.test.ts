import { create } from "@bufbuild/protobuf";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { BackendService, Change, type SeatRequest, SpaceFiledOutcome } from "../gen/backend/v1/services_pb";
import { ReactionSchema, VerificationKind } from "../gen/backend/v1/types_pb";
import { type DeviceSettings, DeviceSettingsSchema, ThemeScope } from "../gen/device/v1/device_pb";
import { unreadOf } from "./rooms";
import { deviceAgain, Session } from "./session";
import { type Key, withKey } from "./settings";
import { Kithd, room, settled, unread } from "./testing/kithd";
import { message, row } from "./testing/rows";

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
  backend.followStream,
  backend.changed,
  backend.configChanged,
  backend.messages,
  backend.verifications,
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
    expect(kithd.calls).toEqual(["Device:", "Theme:"]); // the relay's, which no profile scopes
    expect(kithd.refused).toEqual([]);

    await session.choose("Work");
    await settled();
    expect(view().profile).toBe("Work");
    expect(view().seat.state).toBe("seated");
    expect(kithd.remembered).toBe("Work");
    expect(kithd.refused).toEqual([]);
    for (const c of kithd.calls.slice(3).filter((c) => c !== "Theme:")) {
      expect(c).toMatch(/:Work$/);
    }
    expect(kithd.open().map((s) => s.profile)).toEqual(Array(9).fill("Work"));
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

  it("is the bridge's on a device that joined one, and the window says it is connected there", async () => {
    kithd.joined = "home";
    kithd.profiles = ["Solo"];
    await started();
    expect(view().bridge).toBe("home");
    expect(view().socket).toBe("");
    expect(view().profile).toBe("Solo");
    expect(view().seat.state).toBe("seated");
  });

  it("is read again until a bridge that did not answer at start does", async () => {
    kithd.joined = "home";
    kithd.profiles = ["Solo"];
    kithd.down();
    await started();
    expect(view().profile).toBe("");
    expect(view().problem).toContain("not reachable");

    kithd.again();
    await new Promise((resolve) => setTimeout(resolve, deviceAgain));
    await settled();
    expect(view().profile).toBe("Solo");
    expect(view().seat.state).toBe("seated");
    expect(view().problem).toBeUndefined();
    expect(kithd.calls.filter((c) => c === "Device:").length).toBe(2);
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
    expect(kithd.calls.filter((c) => !c.startsWith("Device") && !c.startsWith("Theme"))).toEqual([]);

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

  /** lost is a deletion of r's newest message the stream lost: the cache holds it, no frame says so. */
  const lost = () => {
    const all = kithd.of("Home").messages.get(r) ?? [];
    const last = all.at(-1);
    if (last !== undefined) {
      kithd.of("Home").messages.set(r, [...all.slice(0, -1), message({ id: last.id, ts: 30, redacted: true }, r)]);
    }
  };
  const pages = () => kithd.calls.filter((c) => c.startsWith("CachedTimeline")).length;
  const waited = async () => {
    await new Promise((resolve) => setTimeout(resolve, deviceAgain));
    await settled();
  };

  it("is read again after a wait while its reload's pages fail, and says so meanwhile", async () => {
    seed(3);
    await started();
    session.open(r);
    await settled();
    lost();
    kithd.failPages = 2;
    kithd.readAgain("Home");
    await settled();
    expect(view().problem).toContain("database is locked");
    expect(view().timeline?.rows.at(-1)?.redacted).toBe(false); // the rows shown stay meanwhile
    expect(pages()).toBe(2);
    await waited();
    expect(pages()).toBe(3); // one wait, one ask
    expect(view().timeline?.rows.at(-1)?.redacted).toBe(false);
    await waited();
    expect(pages()).toBe(4);
    expect(ids()).toEqual(["$1", "$2", "$3"]);
    expect(view().timeline?.rows.at(-1)?.redacted).toBe(true);
    await waited();
    expect(pages()).toBe(4); // read, nothing is asked again
  });

  it("leaves a reload that failed in a room left behind: the room opened is read as any, the other afresh", async () => {
    seed(3);
    kithd.write("Home", message({ id: "$o", ts: 5 }, "!other"));
    await started();
    session.open(r);
    await settled();
    lost();
    kithd.failPages = 1;
    kithd.readAgain("Home");
    await settled();
    session.open("!other");
    await settled();
    await waited();
    expect(ids()).toEqual(["$o"]);
    expect(pages()).toBe(3); // the reload, and the room opened
    session.open(r);
    await settled();
    expect(ids()).toEqual(["$1", "$2", "$3"]);
    expect(view().timeline?.rows.at(-1)?.redacted).toBe(true);
  });

  it("asks nothing in a profile left while its reload waited", async () => {
    kithd.profiles = ["Home", "Work"];
    kithd.remembered = "Home";
    kithd.of("Work").rooms = [room(r)];
    kithd.write("Work", message({ id: "$w", ts: 5 }, r));
    seed(3);
    await started();
    session.open(r);
    await settled();
    kithd.failPages = 1;
    kithd.readAgain("Home");
    await settled();
    await session.choose("Work");
    await settled();
    session.open(r);
    await settled();
    await waited();
    expect(ids()).toEqual(["$w"]);
    expect(kithd.calls.filter((c) => c === "CachedTimeline:Home")).toHaveLength(2);
    expect(kithd.calls.filter((c) => c === "CachedTimeline:Work")).toHaveLength(1);
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
    kithd.settings = create(DeviceSettingsSchema, {
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
    kithd.settings = create(DeviceSettingsSchema, {
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

describe("the settings page", () => {
  const r = "!r";
  const page = () => view().settingsPage;
  /** opened is the window seated with r open and watched, and the settings page open. */
  const opened = async () => {
    kithd.of("Home").rooms = [room(r)];
    kithd.write("Home", message({ id: "$1", ts: 10, sender: "@alice" }, r));
    await started();
    session.open(r);
    session.watch(true);
    session.openSettings();
    await settled();
  };
  /** elsewhere is another writer of device.toml (the TUI's editor, a hand edit). */
  const elsewhere = <K extends Key>(key: K, value: DeviceSettings[K]) => {
    if (kithd.settings !== "fail") {
      kithd.settings = withKey(kithd.settings, key, value);
      kithd.settingsRevision++;
    }
  };

  it("writes each change at once, and the open room follows it", async () => {
    await opened();
    expect(page()?.read?.settings.sendReceipts).toBe(true);
    await session.changeSetting("sendReceipts", false);
    await session.changeSetting("unreadLine", false);
    await settled();
    expect(kithd.settings).toMatchObject({ sendReceipts: false, unreadLine: false });
    expect(page()).toMatchObject({ busy: false, note: "" });
    expect(view().unreadLine).toBe(false);
    kithd.write("Home", message({ id: "$2", ts: 20, sender: "@alice" }, r));
    await settled();
    expect(kithd.receipts).toEqual([`${r} $1 public`, `${r} $2 private`]);
  });

  it("writes a change onto another writer's of a different setting, both kept", async () => {
    await opened();
    kithd.beforeSettingsWrite = () => {
      elsewhere("markdown", false);
    };
    await session.changeSetting("sendTyping", false);
    expect(kithd.settings).toMatchObject({ markdown: false, sendTyping: false });
    expect(page()?.read?.settings).toMatchObject({ markdown: false, sendTyping: false });
    expect(page()?.note).toBe("");
  });

  it("keeps another writer's change of the same setting, and says so", async () => {
    await opened();
    kithd.beforeSettingsWrite = () => {
      elsewhere("reactionsStatic", ["🎉"]);
    };
    await session.changeSetting("reactionsStatic", ["👍"]);
    expect(kithd.settings).toMatchObject({ reactionsStatic: ["🎉"] });
    expect(page()?.read?.settings.reactionsStatic).toEqual(["🎉"]);
    expect(page()?.note).toMatch(/changed elsewhere/);
  });

  it("says a change was not saved, and the open room keeps its settings", async () => {
    await opened();
    kithd.settings = "fail";
    await session.changeSetting("unreadLine", false);
    expect(page()?.note).toMatch(/not saved.*does not load/);
    expect(page()?.read?.settings.unreadLine).toBe(true);
    expect(view().unreadLine).toBe(true);
  });
});

describe("the theme", () => {
  const value = (key: string) => view().theme?.values[key]?.value;

  it("is read at the start and after each config change; one that cannot be read leaves the last", async () => {
    kithd.themeShared.set("dark.accent", "#111111");
    await started();
    expect(value("dark.accent")).toBe("#111111");

    kithd.themeShared.set("dark.accent", "#222222"); // the TUI's editor, through kithd
    kithd.configRevision++;
    kithd.send(backend.configChanged, "Home", { config: "" });
    await settled();
    expect(value("dark.accent")).toBe("#222222");

    kithd.themeFails = true; // then edited by hand into one that does not check
    kithd.send(backend.configChanged, "Home", { config: "" });
    await settled();
    expect(value("dark.accent")).toBe("#222222");
  });

  it("is read by a window started again after a stop, as React's StrictMode starts it", async () => {
    kithd.themeShared.set("mode", "dark");
    session.start();
    session.stop();
    await started();
    expect(value("mode")).toBe("dark");
  });

  it("writes a key everywhere, this device's over it, and its reset shows every device's again", async () => {
    await started();
    await session.changeTheme("light.accent", ThemeScope.SHARED, "#123456");
    await session.changeTheme("light.accent", ThemeScope.DEVICE, "#654321");
    expect([kithd.themeShared.get("light.accent"), kithd.themeDevice.get("light.accent")]).toEqual([
      "#123456",
      "#654321",
    ]);
    expect(value("light.accent")).toBe("#654321");
    await session.changeTheme("light.accent", ThemeScope.DEVICE, "");
    expect(value("light.accent")).toBe("#123456");
    expect(view().themeChange).toEqual({ busy: false, note: "" });
  });

  it("keeps another writer's change of the same key, and says so", async () => {
    await started();
    kithd.themeShared.set("mode", "light"); // another window, after this one read the theme
    kithd.configRevision++;
    await session.changeTheme("mode", ThemeScope.SHARED, "dark");
    expect(kithd.themeShared.get("mode")).toBe("light");
    expect(value("mode")).toBe("light");
    expect(view().themeChange.note).toMatch(/changed elsewhere/);
  });

  it("reads the open room's sender colors", async () => {
    kithd.of("Home").rooms = [room("!a")];
    kithd.slots.set("!a", new Map([["@ann:x", 3]]));
    await started();
    session.open("!a");
    await settled();
    expect(view().slots.get("@ann:x")).toBe(3);
  });
});

describe("search, jumps, and back and forward", () => {
  /** words writes n messages into room in profile, saying hello and bye in turn. */
  const words = (profile: string, r: string, n: number) => {
    for (let i = 1; i <= n; i++) {
      const body = `${i % 2 === 0 ? "hello" : "bye"} from ${r}`;
      kithd.write(profile, message({ id: `$${r}-${String(i)}`, ts: i * 10, body }, r));
    }
  };

  it("finds what is typed where it is asked, and a hit opens its room read back to the message", async () => {
    kithd.of("Home").rooms = [room("!a"), room("!b")];
    words("Home", "!a", 12);
    words("Home", "!b", 2);
    await started();

    session.search("hello", "all");
    await settled();
    expect(view().search?.hits.map((h) => h.eventId)).toEqual([
      "$!a-12",
      "$!a-10",
      "$!a-8",
      "$!a-6",
      "$!a-4",
      "$!b-2",
      "$!a-2",
    ]);
    session.open("!b");
    session.search("hello", "room");
    await settled();
    expect(view().search?.hits.map((h) => h.eventId)).toEqual(["$!b-2"]);
    session.search("from:role:attorney", "all");
    expect(view().search?.note).toContain("phonebook");
    expect(kithd.calls.filter((c) => c.startsWith("SearchMessages"))).toHaveLength(2);

    session.jump("!a", "$!a-2"); // older than the room's newest page
    await settled();
    expect(view().open).toBe("!a");
    expect(view().timeline?.target).toBe("$!a-2");
    expect(view().timeline?.rows.map((m) => m.id)).toContain("$!a-2");
    session.jump("!a", "$!a-99");
    await settled();
    expect(view().problem).toBe("That message is no longer cached");

    session.back();
    await settled();
    expect(view().open).toBe("!b");
    expect(view().forward).toBe(true);
    session.forward();
    await settled();
    expect(view().open).toBe("!a");
  });

  it("go back within the profile the window runs in, and find their way again when it returns", async () => {
    kithd.profiles = ["Home", "Work"];
    kithd.remembered = "Home";
    kithd.of("Home").rooms = [room("!a"), room("!b")];
    kithd.of("Work").rooms = [room("!w"), room("!v")];
    await started();
    session.open("!a");
    session.open("!b");
    await session.choose("Work");
    await settled();
    session.open("!w");
    expect(view().back).toBe(false);
    await session.choose("Home");
    await settled();
    session.back();
    await settled();
    expect(view().open).toBe("!a");
  });

  it("shows no hit, and opens no room, the profile no longer holds", async () => {
    kithd.of("Home").rooms = [room("!a"), room("!b")];
    words("Home", "!a", 2);
    words("Home", "!b", 2);
    await started();
    session.search("hello", "all");
    await settled();
    expect(view().search?.hits.map((h) => h.roomId)).toEqual(["!b", "!a"]);

    kithd.of("Home").rooms = [room("!b")];
    kithd.send(backend.changed, "Home", { change: Change.ROOMS });
    await settled();
    expect(view().search?.hits.map((h) => h.roomId)).toEqual(["!b"]);
    session.jump("!a", "$!a-2");
    await settled();
    expect(view().open).toBe("");
    expect(view().problem).toBe("That room is no longer in this profile");
  });

  // The window searches, jumps, goes back and forward, and switches profiles, while
  // searches are answered late and rooms leave and join the profiles; a hit clicked may
  // be one drawn before any of that. Whenever all is quiet, the window shows nothing of
  // a room outside the profile it runs in: no room open, no hit, and the hits are the
  // last search's.
  it.each(Array.from({ length: 40 }, (_, i) => i + 1))(
    "stay in the window's profile over every switch (seed %i)",
    async (seed) => {
      const pick = rng(seed);
      const all = { Home: ["!h0", "!h1", "!both"], Work: ["!w0", "!w1", "!both"] };
      kithd.profiles = ["Home", "Work"];
      kithd.remembered = "Home";
      for (const [profile, ids] of Object.entries(all)) {
        kithd.of(profile).rooms = ids.map((id) => room(id));
        for (const id of ids) {
          words(profile, id, 6);
        }
      }
      await started();
      const drawn: { roomId: string; eventId: string }[] = [];
      let asked = "";
      const check = (when: string) => {
        const v = view();
        const holds = new Set(kithd.of(v.profile).rooms.map((r) => r.id));
        if (v.open !== "") {
          expect(holds.has(v.open), `${v.open} open in ${v.profile}, ${when}`).toBe(true);
        }
        if (v.search !== undefined) {
          expect(`${v.search.query} ${v.search.scope}`, `the search shown, ${when}`).toBe(asked);
        }
        for (const h of v.search?.hits ?? []) {
          expect(holds.has(h.roomId), `a hit in ${h.roomId} in ${v.profile}, ${when}`).toBe(true);
          expect(h.snippet, when).toContain(v.search?.query);
        }
      };
      for (let s = 0; s < 80; s++) {
        for (let i = pick(3); i > 0; i--) {
          await new Promise((resolve) => setTimeout(resolve, 0));
        }
        const profile = view().profile;
        const ids = all[profile as keyof typeof all];
        switch (pick(10)) {
          case 0:
            // A room picked: one listed, or one of the other profile's still on screen.
            session.open(pick(3) === 0 ? (Object.values(all).flat()[pick(6)] ?? "") : (ids[pick(3)] ?? ""));
            break;
          case 1:
            void session.choose(pick(2) === 0 ? "Home" : "Work");
            break;
          case 2:
            session.back();
            break;
          case 3:
            session.forward();
            break;
          case 4: {
            const query = ["hello", "bye", "from"][pick(3)] ?? "";
            const scope = (["room", "row", "all"] as const)[pick(3)] ?? "all";
            asked = `${query} ${scope}`;
            session.search(query, scope);
            drawn.push(...(view().search?.hits ?? []));
            break;
          }
          case 5: {
            // A hit clicked: one shown now, or one drawn before (another profile's, a search ago).
            const hit = pick(2) === 0 ? view().search?.hits[pick(4)] : drawn[pick(drawn.length + 1)];
            if (hit !== undefined) {
              session.jump(hit.roomId, hit.eventId);
            }
            break;
          }
          case 6: {
            // A room leaves the profile, or comes back; the window is told.
            const p = pick(2) === 0 ? "Home" : "Work";
            const id = all[p][pick(3)] ?? "";
            const rooms = kithd.of(p).rooms;
            const open = view().profile === p && view().open === id;
            kithd.of(p).rooms = rooms.some((r) => r.id === id)
              ? open
                ? rooms // the open room stays: leaving it is not this test's
                : rooms.filter((r) => r.id !== id)
              : [...rooms, room(id)];
            kithd.send(backend.changed, p, { change: Change.ROOMS });
            await settled();
            break;
          }
          case 7:
            kithd.holding.add("SearchMessages");
            break;
          case 8:
            kithd.releaseAt(pick(4));
            break;
          default:
            kithd.holding.clear();
            kithd.release(pick(2) === 0);
            await settled();
            drawn.push(...(view().search?.hits ?? []));
            check(`seed ${String(seed)}, step ${String(s)}`);
        }
      }
      kithd.holding.clear();
      kithd.release();
      await settled();
      check(`seed ${String(seed)}, at the end`);
    },
  );
});

describe("a link handed to the window", () => {
  it("opens the chat it names in the profile, at its message, and nothing outside it", async () => {
    kithd.of("Home").rooms = [room("!a"), room("!b")];
    for (let i = 1; i <= 8; i++) {
      kithd.write("Home", message({ id: `$a${String(i)}`, ts: i * 10 }, "!a"));
    }
    await started();
    expect(kithd.follow("Home", "!b")).toBeDefined();
    await settled();
    expect(view().open).toBe("!b");
    expect(kithd.presented.get([...kithd.presented.keys()][0] ?? "")).toBe("!b");

    kithd.follow("Home", "!a/$a1");
    await settled();
    expect(view().open).toBe("!a");
    expect(view().timeline?.target).toBe("$a1");

    kithd.follow("Home", "!elsewhere");
    await settled();
    expect(view().open).toBe("!a");
    expect(view().problem).toBe("That link names no chat in Home");
    expect(JSON.stringify(view(), (_, v: unknown) => (typeof v === "bigint" ? String(v) : v))).not.toContain(
      "!elsewhere",
    );
  });

  // A switch from Home to Work, Work's seat free or held by another window (then taken
  // over), its rooms read at once or late; a link sent on Home's or Work's stream at
  // every turn of the event loop from before the switch is asked to after it settled:
  // while main has moved and the window has not, while the seat is asked for or held,
  // before the rooms are read. Each link handed to it reaches it, and is followed in Work if
  // its stream's profile named the chat and Work holds it, and else opens nothing.
  const points = Array.from({ length: 12 }, (_, at) => at);
  const cases = points.flatMap((at) =>
    [false, true].flatMap((taken) =>
      ["Home", "Work"].flatMap((on) => ["!h0", "!w0", "!both"].map((to) => ({ at, taken, on, to }))),
    ),
  );
  it.each(cases)(
    "sent at turn $at on $on's stream to $to, Work's seat taken: $taken",
    async ({ at, taken, on, to }) => {
      const all = { Home: ["!h0", "!both"], Work: ["!w0", "!both"] };
      kithd.profiles = ["Home", "Work"];
      kithd.remembered = "Home";
      for (const [profile, ids] of Object.entries(all)) {
        kithd.of(profile).rooms = ids.map((id) => room(id));
      }
      await started();
      if (taken) {
        kithd.seats.set("Work", "other");
      }
      if (at % 2 === 1) {
        kithd.holding.add("Rooms");
      }
      void session.choose("Work");
      for (let i = 0; i < at; i++) {
        await new Promise((resolve) => setTimeout(resolve, 0));
      }
      const link = kithd.follow(on, to);
      await settled();
      if (taken) {
        session.takeOver();
        await settled();
      }
      kithd.holding.clear();
      kithd.release();
      await settled();
      expect(view().profile).toBe("Work");
      expect(view().seat.state).toBe("seated");
      // Handed to a stream that was open, the link must reach the window: none is lost.
      const handed = link !== undefined;
      expect(handed && !kithd.linksArrived.includes(link)).toBe(false);
      const follows = handed && link.chats.length > 0 && all.Work.includes(to);
      expect(view().open).toBe(follows ? to : "");
      if (handed && !follows) {
        expect(view().problem).toBe("That link names no chat in Work");
      }
    },
  );

  // Links arrive at every point of a profile switch: before it is asked, while main has
  // moved and the window has not, while the new profile's seat is asked for or held by
  // another window, before its rooms are read; and the window steps aside and takes
  // over. Whenever all is quiet no room outside the profile is open, and at the end the
  // last link that reached the window was followed where the profile allows, and
  // refused where it does not.
  it.each(Array.from({ length: 20 }, (_, i) => i + 1))(
    "is followed in the profile the window ends in, and only there (seed %i)",
    async (seed) => {
      const pick = rng(seed);
      const all = { Home: ["!h0", "!h1", "!both"], Work: ["!w0", "!w1", "!both"] };
      const every = [...new Set(Object.values(all).flat())];
      kithd.profiles = ["Home", "Work"];
      kithd.remembered = "Home";
      for (const [profile, ids] of Object.entries(all)) {
        kithd.of(profile).rooms = ids.map((id) => room(id));
        for (const id of ids) {
          kithd.write(profile, message({ id: `$${id}`, ts: 10 }, id));
        }
      }
      await started();
      const check = (when: string) => {
        const v = view();
        if (v.open !== "") {
          const holds = kithd.of(v.profile).rooms.map((r) => r.id);
          expect(holds, `${v.open} open in ${v.profile}, ${when}`).toContain(v.open);
        }
      };
      // When each link was sent, and when the window was last moved (a switch asked, a
      // seat taken): a link followed before a later move is the person's to leave.
      const sent = new Map<object, number>();
      let moved = 0;
      for (let s = 0; s < 60; s++) {
        for (let i = pick(3); i > 0; i--) {
          await new Promise((resolve) => setTimeout(resolve, 0));
        }
        switch (pick(9)) {
          case 0:
          case 1:
            moved = s;
            void session.choose(pick(2) === 0 ? "Home" : "Work");
            break;
          case 2: {
            // Another window sits in a profile, or takes this one's seat over.
            const p = pick(2) === 0 ? "Home" : "Work";
            moved = s;
            if (kithd.seats.get(p) === undefined) {
              kithd.seats.set(p, "other");
            } else {
              kithd.otherTakesOver(p);
            }
            break;
          }
          case 3:
            moved = s;
            session.takeOver();
            break;
          case 4:
          case 5:
          case 6: {
            const id = every[pick(every.length)] ?? "";
            const frame = kithd.follow(pick(2) === 0 ? "Home" : "Work", pick(2) === 0 ? id : `${id}/$${id}`);
            if (frame !== undefined) {
              sent.set(frame, s);
            }
            break;
          }
          case 7:
            kithd.holding.add("Rooms");
            break;
          default:
            kithd.holding.clear();
            kithd.release(pick(2) === 0);
            await settled();
            check(`seed ${String(seed)}, step ${String(s)}`);
        }
      }
      kithd.holding.clear();
      kithd.release();
      await settled();
      if (view().seat.state !== "seated" && view().profile !== "") {
        session.takeOver();
        await settled();
      }
      const when = `seed ${String(seed)}, at the end`;
      check(when);
      const last = kithd.linksArrived.at(-1);
      if (last !== undefined && view().profile !== "" && (sent.get(last) ?? 0) > moved) {
        const [room = ""] = last.uri.split("/");
        const holds = kithd.of(view().profile).rooms.some((r) => r.id === room);
        if (last.chats.length > 0 && holds) {
          expect(view().open, `${last.uri} followed, ${when}`).toBe(room);
        } else {
          expect(view().open, `${last.uri} not followed, ${when}`).not.toBe(room);
        }
      }
    },
  );
});

describe("a thread", () => {
  const r = "!r";
  const write = (id: string, init: Partial<Parameters<typeof message>[0]> = {}) => {
    kithd.write("Home", message({ id, ts: 10, body: id, ...init }, r));
  };

  beforeEach(() => {
    kithd.of("Home").rooms = [room(r)];
  });

  it("opens beside the room with its root and replies, older replies on asking, and the composer writes into it", async () => {
    write("$root", { ts: 10 });
    for (let i = 1; i <= 6; i++) {
      write(`$t${String(i)}`, { ts: 10 + i, threadRoot: "$root" });
    }
    write("$late", { ts: 100 });
    await started();
    session.open(r);
    await settled();
    session.openThread("$root");
    session.watchThread(true);
    await settled();
    // The room's newest four hold three replies; the root is fetched, older replies asked for.
    expect(view().thread?.rows.map((m) => m.id)).toEqual(["$root", "$t4", "$t5", "$t6"]);
    session.olderInThread(); // its newest page: two replies, the room had them
    await settled();
    session.olderInThread();
    await settled();
    expect(view().thread?.rows.map((m) => m.id)).toEqual(["$root", "$t3", "$t4", "$t5", "$t6"]);
    expect(kithd.receipts).toEqual(["!r $root $t6 public"]);

    session.type("in the thread", 13);
    await session.send();
    session.type("still in it", 11);
    await session.send();
    expect(kithd.sent.map((s) => [s.draft.body, s.draft.threadRoot])).toEqual([
      ["in the thread", "$root"],
      ["still in it", "$root"],
    ]);
    session.closeThread();
    session.type("in the room", 11);
    await session.send();
    expect(kithd.sent.at(-1)?.draft.threadRoot).toBe("");
  });

  // The root is edited and deleted, and replies come, while the pane reads and the
  // composer sends: every reply goes into the thread, and the pane ends as the cache
  // holds the thread.
  it.each(Array.from({ length: 20 }, (_, i) => i + 1))(
    "takes replies while its root is edited or deleted (seed %i)",
    async (seed) => {
      const pick = rng(seed);
      write("$root", { ts: 10 });
      write("$t1", { ts: 11, threadRoot: "$root" });
      for (let i = 0; i < 6; i++) {
        write(`$m${String(i)}`, { ts: 20 + i });
      }
      await started();
      session.open(r);
      await settled();
      session.openThread("$root");
      let made = 1;
      let edits = 0;
      for (let s = 0; s < 40; s++) {
        for (let i = pick(3); i > 0; i--) {
          await new Promise((resolve) => setTimeout(resolve, 0));
        }
        switch (pick(7)) {
          case 0:
            edits++;
            write("$root", {
              ts: 10,
              edited: true,
              body: `root ${String(edits)}`,
              editedAt: 1000 + edits,
              revisionId: `$e${String(edits)}`,
            });
            break;
          case 1:
            write("$root", { ts: 10, redacted: true, body: "" });
            break;
          case 2:
            made++;
            write(`$t${String(made)}`, { ts: 30 + made, threadRoot: "$root" });
            break;
          case 3:
            session.type(`reply ${String(s)}`, 0);
            void session.send();
            break;
          case 4:
            kithd.holding.add(pick(2) === 0 ? "FetchEvent" : "ThreadPage");
            break;
          case 5:
            session.olderInThread();
            break;
          default:
            kithd.holding.clear();
            kithd.release(pick(2) === 0);
        }
      }
      kithd.holding.clear();
      kithd.release();
      await settled();
      for (let i = 0; i < 8; i++) {
        session.olderInThread();
        await settled();
      }
      const when = `seed ${String(seed)}`;
      expect(kithd.sent.length, when).toBeGreaterThan(0);
      for (const sent of kithd.sent) {
        expect(sent.draft.threadRoot, when).toBe("$root");
      }
      const cache = kithd.of("Home").messages.get(r) ?? [];
      const want = cache.filter((m) => m.id === "$root" || m.threadRoot === "$root").map(row);
      expect(view().thread?.rows.map(row), when).toEqual(want);
    },
  );
});

describe("an attachment", () => {
  it("is saved through main, by its room, event and name", async () => {
    kithd.of("Home").rooms = [room("!r")];
    await started();
    await session.save(message({ id: "$1", media: "cat.png" }, "!r"));
    expect(kithd.saved).toEqual(["!r $1 cat.png"]);
  });
});

describe("the settings page's accounts", () => {
  const opened = async () => {
    kithd.loginNetworks = { networks: [{ network: "whatsapp", label: "WhatsApp", accounts: [{ name: "main" }] }] };
    await started();
    session.openSettings();
    await settled();
  };

  it("leads a login through its steps, its account written, then reads the rail and rooms again", async () => {
    await opened();
    const network = view().login.networks?.[0];
    expect(network?.accounts.map((a) => a.name)).toEqual(["main"]);
    kithd.loginSteps.push(
      { login: "l1", ask: [{ key: "phone", label: "Phone" }] },
      { login: "l1", configure: { table: "whatsapp.account", values: { name: "work" } } },
      { login: "l1", qr: "2@x" },
      { login: "l1", done: "logged in to WhatsApp as work" },
    );
    if (network === undefined) {
      throw new Error("no network");
    }
    session.beginLogin(network, "");
    await settled();
    expect(view().login.run?.ask.map((f) => f.key)).toEqual(["phone"]);
    const before = kithd.calls.filter((c) => c.startsWith("Rail")).length;
    session.answerLogin({ phone: "+1555" });
    await settled();
    expect(kithd.accountsWritten).toEqual(["whatsapp.account work"]);
    expect(view().login.run).toBeUndefined();
    expect(view().login.note).toBe("logged in to WhatsApp as work");
    expect(kithd.calls.filter((c) => c.startsWith("Rail")).length).toBeGreaterThan(before);
  });

  it("signs out of an account, its rooms forgotten when asked", async () => {
    await opened();
    await session.signOut("telegram:1/account", "Telegram", true);
    expect(kithd.signedOut).toEqual(["telegram:1/account forget"]);
    expect(view().login.note).toBe("Signed out of Telegram.");
  });
});

describe("verifying this session", () => {
  const step = (kind: VerificationKind, txnId: string, reason = "") => {
    kithd.send(backend.verifications, "Home", {
      verification: {
        kind,
        txnId,
        from: "@me:x",
        device: "PHONE",
        reason,
        emojis: kind === VerificationKind.SAS ? [{ glyph: "🐶", name: "Dog" }] : [],
      },
    });
  };
  const answers = () => kithd.writes.filter((w) => /Verification|ConfirmSAS/.test(w));

  it("answers a request from another session: accepted, the emoji confirmed, verified", async () => {
    await started();
    step(VerificationKind.REQUESTED, "t1");
    await settled();
    expect(view().verify.flow).toMatchObject({ txn: "t1", from: "@me:x", device: "PHONE", stage: "requested" });
    session.answerVerify();
    await settled();
    step(VerificationKind.ACCEPTED, "t1");
    step(VerificationKind.SAS, "t1");
    await settled();
    expect(view().verify.flow?.emojis.map((e) => e.name)).toEqual(["Dog"]);
    session.answerVerify();
    await settled();
    step(VerificationKind.DONE, "t1");
    await settled();
    expect(view().verify).toMatchObject({ flow: undefined, said: ["This session is verified."] });
    expect(answers()).toEqual(["AcceptVerification t1", "ConfirmSAS t1"]);
  });

  it("asks its other sessions from the account picked, and can only call that off until one accepts", async () => {
    await started();
    expect(view().accounts.map((a) => a.on)).toEqual(["matrix:1/"]);
    session.verify("matrix:1/");
    await settled();
    expect(view().verify.flow).toMatchObject({ txn: "ours", ours: true });
    session.answerVerify(); // nothing to accept: the other session does
    step(VerificationKind.SAS, "ours");
    await settled();
    session.answerVerify();
    await settled();
    expect(answers()).toEqual(["StartVerification matrix:1/", "ConfirmSAS ours"]);
  });

  it("does not answer what another window answered first", async () => {
    await started();
    step(VerificationKind.REQUESTED, "t1");
    step(VerificationKind.ACCEPTED, "t1");
    await settled();
    session.answerVerify();
    await settled();
    expect(answers()).toEqual([]);
    expect(view().verify.flow).toMatchObject({ answered: true, mine: false });
  });

  it("ends when the other session cancels mid-compare, and a confirm failing after changes nothing", async () => {
    await started();
    step(VerificationKind.REQUESTED, "t1");
    step(VerificationKind.SAS, "t1");
    await settled();
    kithd.verifyFails.set("ConfirmSAS", "unknown transaction ID");
    kithd.holding.add("ConfirmSAS");
    session.answerVerify();
    await settled();
    step(VerificationKind.CANCELED, "t1", "verification timed out");
    await settled();
    kithd.release();
    await settled();
    expect(view().verify).toMatchObject({ flow: undefined, said: ["Verification canceled: verification timed out"] });
  });

  it("ends when kithd restarts mid-verification, and answers nothing after", async () => {
    await started();
    step(VerificationKind.REQUESTED, "t1");
    step(VerificationKind.SAS, "t1");
    await settled();
    kithd.down();
    await settled();
    kithd.again();
    await settled();
    expect(view().verify.flow).toBeUndefined();
    expect(view().verify.said[0]).toMatch(/lost mid-verification/);
    session.answerVerify();
    await settled();
    expect(answers()).toEqual([]);
  });

  it("shows nothing of a verification after the window reloads", async () => {
    await started();
    step(VerificationKind.REQUESTED, "t1");
    await settled();
    session.stop();
    session = new Session();
    await started();
    expect(view().verify.flow).toBeUndefined();
    session.answerVerify();
    await settled();
    expect(answers()).toEqual([]);
  });

  it("declines at once, whatever kithd answers", async () => {
    await started();
    step(VerificationKind.REQUESTED, "t1");
    await settled();
    kithd.verifyFails.set("CancelVerification", "kithd is not reachable");
    session.cancelVerify();
    expect(view().verify).toMatchObject({ flow: undefined, said: ["Verification canceled."] });
    await settled();
    expect(view().problem).toBeUndefined();
    expect(answers()).toEqual(["CancelVerification t1"]);
  });
});

describe("filing a room", () => {
  const alpha = () => room("!a", { name: "Alpha" });
  beforeEach(() => {
    kithd.profiles = ["Home", "Work"];
    kithd.remembered = "Home";
    kithd.of("Home").rooms = [alpha()];
    kithd.of("Work").rooms = [room("!w", { name: "Desk" })];
    kithd.places.set("!a", [
      { tag: "Pinned", label: "Pinned", in: true },
      { tag: "Later", label: "Later" },
      { space: "!work", label: "Job", in: true },
    ]);
  });

  it("offers kithd's places ticked, with this device's filing_spaces, and files only what changed", async () => {
    kithd.settings = create(DeviceSettingsSchema, { sendReceipts: true, filingSpaces: ["space:Work"] });
    kithd.fileAnswer = { spaces: [{ spaceId: "!work", outcome: SpaceFiledOutcome.FILED }] };
    await started();
    session.openFiling("!a");
    await settled();
    expect(kithd.filingSpacesAsked).toEqual([["space:Work"]]);
    expect(view().filing?.places?.map((p) => `${p.label} ${String(p.in)}`)).toEqual([
      "Pinned true",
      "Later false",
      "Job true",
    ]);
    expect([...(view().filing?.ticked ?? [])].sort()).toEqual(["space !work", "tag Pinned"]);
    session.tickFiling("tag Later");
    session.tickFiling("space !work");
    session.tickFiling("tag Pinned");
    session.tickFiling("tag Pinned"); // and back: nobody's change
    session.applyFiling();
    expect(view().filing?.busy).toBe(true);
    await settled();
    expect(kithd.filed).toEqual(["!a Later+ !work-:Home"]);
    expect(view().filing).toBeUndefined();
    expect(view().filingNote).toBe("Alpha is in Later · taken out of Job");
  });

  it("asks kithd nothing when no tick changed, and nothing for a room the profile does not hold", async () => {
    await started();
    session.openFiling("!w"); // Work's
    await settled();
    expect(view().filing).toBeUndefined();
    session.openFiling("!a");
    await settled();
    session.applyFiling();
    await settled();
    expect(kithd.filed).toEqual([]);
    expect(kithd.calls.filter((c) => c.startsWith("FileRoom") || c.startsWith("FilingPlaces"))).toEqual([
      "FilingPlaces:Home",
    ]);
    expect(view().filing).toBeUndefined();
    expect(view().filingNote).toBe("No change.");
  });

  it("refused, keeps the picker open with its ticks and says why; refused by the network, says so", async () => {
    await started();
    session.openFiling("!a");
    await settled();
    session.tickFiling("tag Later");
    kithd.fileRefusal = "[not_found] daemon: the profile Home holds no tag Later";
    session.applyFiling();
    await settled();
    expect(view().filing?.busy).toBe(false);
    expect(view().filing?.note).toMatch(/holds no tag Later/);
    expect(view().filing?.ticked.has("tag Later")).toBe(true);
    kithd.fileRefusal = "";
    kithd.fileAnswer = { archiveFailed: "telegram: not now" };
    session.applyFiling();
    await settled();
    expect(view().filing).toBeUndefined();
    expect(view().filingNote).toBe(
      "Alpha is in Later · its network did not archive or unarchive it: telegram: not now",
    );
  });

  it("left for another profile while it is read or filed, shows nothing of it there", async () => {
    await started();
    kithd.holding.add("FilingPlaces");
    session.openFiling("!a");
    await settled();
    await session.choose("Work");
    await settled();
    expect(view().filing).toBeUndefined();
    kithd.places.set("!w", [{ tag: "Desk", label: "Desk" }]);
    session.openFiling("!w"); // Work's own, asked before Home's answer lands
    await settled();
    kithd.release(true);
    await settled();
    expect(view().profile).toBe("Work");
    expect(view().filing?.places?.map((p) => p.label)).toEqual(["Desk"]);
    session.closeFiling();

    kithd.holding.delete("FilingPlaces");
    await session.choose("Home");
    await settled();
    session.openFiling("!a");
    await settled();
    session.tickFiling("tag Later");
    kithd.holding.add("FileRoom");
    session.applyFiling();
    await settled();
    await session.choose("Work");
    await settled();
    kithd.release();
    await settled();
    expect(kithd.filed).toEqual(["!a Later+:Home"]); // filed where it was asked
    expect(view().filing).toBeUndefined();
    expect(view().filingNote).toBe("");
    expect(view().problem).toBeUndefined();
  });

  it("shows nothing of a filing after the window reloads, which kithd finishes all the same", async () => {
    await started();
    session.openFiling("!a");
    await settled();
    session.tickFiling("tag Later");
    kithd.holding.add("FileRoom");
    session.applyFiling();
    await settled();
    session.stop();
    session = new Session();
    await started();
    kithd.release();
    await settled();
    expect(kithd.filed).toEqual(["!a Later+:Home"]);
    expect(view().filing).toBeUndefined();
    expect(view().filingNote).toBe("");
  });

  it("closed while kithd answers its places, stays closed", async () => {
    await started();
    kithd.holding.add("FilingPlaces");
    session.openFiling("!a");
    await settled();
    session.closeFiling();
    kithd.release();
    await settled();
    expect(view().filing).toBeUndefined();
  });
});
