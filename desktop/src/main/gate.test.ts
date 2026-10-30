import { hostname } from "node:os";

import { create, type DescMessage, type MessageInitShape, toBinary } from "@bufbuild/protobuf";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { BackendService, type SeatRequest } from "../gen/backend/v1/services_pb";
import { DeviceService } from "../gen/device/v1/device_pb";
import type { StreamEvent } from "../shared/ipc";
import { type Frame, Gate, Refused } from "./gate";
import { FakeKithd, until } from "./testing/kithd";

const window: Frame = { url: "app://kith/index.html", parent: null };
const backend = BackendService.method;

/** bytes is a request as the window sends it. */
function bytes<I extends DescMessage>(schema: I, init: MessageInitShape<I> = {} as MessageInitShape<I>): Uint8Array {
  return toBinary(schema, create(schema, init));
}

const empty = new Uint8Array();

let kithd: FakeKithd;
let gate: Gate;

beforeEach(async () => {
  kithd = new FakeKithd();
  kithd.profiles = ["Home", "Work"];
  kithd.remembered = "Work";
  kithd.invited = ["!in:x"];
  await kithd.start();
  gate = new Gate(kithd.transport());
});

afterEach(async () => {
  await kithd.dispose();
});

/** outcome is what the gate did with a call: sent to kithd (answered or not), or refused before it. */
async function outcome(frame: Frame | null, name: unknown, request: unknown): Promise<"answered" | "refused"> {
  try {
    await gate.call(frame, name, request);
    return "answered";
  } catch (err) {
    if (err instanceof Refused) {
      return "refused";
    }
    return "answered"; // kithd's own error: the call reached it
  }
}

/** opened is what the gate did with a stream: kept it open, or refused it. */
function opened(frame: Frame | null, name: unknown, request: unknown): "opened" | "refused" {
  try {
    gate.subscribe(frame, name, request, () => undefined)();
    return "opened";
  } catch (err) {
    if (err instanceof Refused) {
      return "refused";
    }
    throw err;
  }
}

/** listen opens the stream named, keeping what it posts. */
function listen(name: string, request: Uint8Array = empty) {
  const events: StreamEvent[] = [];
  const stop = gate.subscribe(window, name, request, (e) => events.push(e));
  return { events, stop };
}

/** device reads the device through the gate, as the window does first. */
async function device(): Promise<void> {
  await gate.call(window, DeviceService.method.device.name, empty);
}

// The window's whole scope: a call or stream joins the list only with the PR that needs it.
const allowedCalls = [
  "Status",
  "Rooms",
  "LastMessages",
  "CachedUnread",
  "CachedInvites",
  "JoinRoom",
  "LeaveRoom",
  "Device",
  "ChooseProfile",
];
const allowedStreams = ["Changed", "ConfigChanged", "Seat", "UnreadStream", "Invites"];

// A request each listed call is answered for: an invitation's room, a profile the config has.
const requests: Record<string, Uint8Array> = {
  JoinRoom: bytes(backend.joinRoom.input, { roomIdOrAlias: "!in:x" }),
  LeaveRoom: bytes(backend.leaveRoom.input, { roomId: "!in:x" }),
  ChooseProfile: bytes(DeviceService.method.chooseProfile.input, { name: "Work" }),
};

// Every name the window could send: each method of both services by its name and in
// other spellings, and names an object map would answer for itself.
const names: unknown[] = [
  ...[...BackendService.methods, ...DeviceService.methods].flatMap((m) => [
    m.name,
    m.localName,
    `${m.parent.typeName}/${m.name}`,
    `/${m.parent.typeName}/${m.name}`,
    ` ${m.name}`,
  ]),
  "constructor",
  "__proto__",
  "toString",
  "hasOwnProperty",
  "",
  42,
  null,
  undefined,
  { toString: () => "Status" },
  ["Status"],
];

describe("the gate", () => {
  it("answers only the calls on the list, and refuses every other name before kithd", async () => {
    await device();
    for (const name of names) {
      const want = typeof name === "string" && allowedCalls.includes(name) ? "answered" : "refused";
      const request = typeof name === "string" ? (requests[name] ?? empty) : empty;
      expect(await outcome(window, name, request), String(name)).toBe(want);
    }
    const reached = new Set(kithd.reached.map((r) => r.split(":")[0]));
    expect([...reached].sort()).toEqual([...allowedCalls].sort());
  });

  it("opens only the streams on the list, and refuses every other name before kithd", async () => {
    await device();
    for (const name of names) {
      const want = typeof name === "string" && allowedStreams.includes(name) ? "opened" : "refused";
      expect(opened(window, name, empty), String(name)).toBe(want);
    }
  });

  // Electron's WebFrameMain: the window's top frame, or anything that is not it.
  const frames: [Frame | null, boolean][] = [
    [window, true],
    [{ url: "app://kith/", parent: null }, true],
    [{ url: "app://kith/index.html", parent: {} }, false],
    [null, false],
    [{ url: "app://evil/index.html", parent: null }, false],
    [{ url: "app://kith.evil/index.html", parent: null }, false],
    [{ url: "app://kith@evil/index.html", parent: null }, false],
    [{ url: "app://kith:1/index.html", parent: null }, false],
    [{ url: "http://kith/index.html", parent: null }, false],
    [{ url: "https://kith/index.html", parent: null }, false],
    [{ url: "file:///index.html", parent: null }, false],
    [{ url: "about:blank", parent: null }, false],
    [{ url: "devtools://devtools/bundled/index.html", parent: null }, false],
    [{ url: "not a url", parent: null }, false],
    [{ url: "", parent: null }, false],
  ];

  it("answers the window's top frame alone", async () => {
    await device();
    for (const [frame, allowed] of frames) {
      const what = JSON.stringify(frame);
      expect(await outcome(frame, "Status", empty), what).toBe(allowed ? "answered" : "refused");
      expect(await outcome(frame, "Device", empty), what).toBe(allowed ? "answered" : "refused");
      expect(opened(frame, "Changed", empty), what).toBe(allowed ? "opened" : "refused");
    }
    expect(kithd.received.filter((r) => r.method === "Status")).toHaveLength(frames.filter(([, ok]) => ok).length);
  });

  it("refuses a request that is not its method's, and what reaches kithd is only the method's own fields", async () => {
    await device();
    // Not bytes at all, then bytes: random (most malformed, some well-formed wire data of
    // fields StatusRequest does not have), and the cases that matter named.
    const requests: unknown[] = ["", "\u0008\u0001", [8, 1], new Uint8Array([8, 1]).buffer, null, undefined, {}, 7];
    let seed = 1;
    const random = () => (seed = (seed * 1103515245 + 12345) % 2 ** 31) / 2 ** 31;
    for (let i = 0; i < 300; i++) {
      requests.push(Uint8Array.from({ length: Math.floor(random() * 12) }, () => Math.floor(random() * 256)));
    }
    requests.push(new Uint8Array(), new Uint8Array([8, 1]), new Uint8Array([0xff]), new Uint8Array([0x0a, 0x05, 1]));

    for (const request of requests) {
      const got = await outcome(window, "Status", request);
      if (!(request instanceof Uint8Array)) {
        expect(got, String(request)).toBe("refused");
      }
    }
    expect(kithd.received.length).toBeGreaterThan(0);
    for (const r of kithd.received) {
      expect(r.message.$unknown, "fields StatusRequest does not have reached kithd").toBeUndefined();
    }
  });

  it("sends nothing to kithd before a profile is chosen, and the one chosen with every call and stream after", async () => {
    // First connect with several profiles, none remembered: kithd would answer a request
    // naming none for the config's first.
    kithd.remembered = "";
    const tryAll = async () => [
      await outcome(window, "Status", empty),
      await outcome(window, "Rooms", empty),
      opened(window, "Changed", empty),
      opened(window, "Seat", empty),
    ];
    expect(await tryAll()).toEqual(["refused", "refused", "refused", "refused"]);
    await device();
    expect(await tryAll()).toEqual(["refused", "refused", "refused", "refused"]);
    expect(
      await outcome(window, "ChooseProfile", bytes(DeviceService.method.chooseProfile.input, { name: "nope" })),
    ).toBe("answered");
    expect(await tryAll()).toEqual(["refused", "refused", "refused", "refused"]);
    expect(kithd.reached).toEqual(["Device:", "ChooseProfile:"]);

    await gate.call(window, "ChooseProfile", bytes(DeviceService.method.chooseProfile.input, { name: "work" }));
    const s = listen("Changed");
    await until(() => s.events.some((e) => e.kind === "attached"), "the stream to attach");
    await gate.call(window, "Status", empty);
    s.stop();
    expect(kithd.reached.slice(3)).toEqual(["Changed:Work", "Status:Work"]);

    // One profile, none remembered: it is run unasked.
    gate.restart();
    expect(await outcome(window, "Status", empty)).toBe("refused");
    kithd.profiles = ["Solo"];
    await device();
    await gate.call(window, "Status", empty);
    expect(kithd.reached.at(-1)).toBe("Status:Solo");
  });

  it("ends every stream of a profile the window leaves, and runs in none when a reload removes its profile", async () => {
    await device();
    const changed = listen("Changed");
    const unread = listen("UnreadStream");
    await until(() => kithd.streams(backend.changed) === 1 && kithd.streams(backend.unreadStream) === 1, "both open");

    // The device file changed under the window (another writer): it stays where it runs.
    kithd.remembered = "Home";
    await device();
    await gate.call(window, "Status", empty);
    expect(kithd.reached.at(-1)).toBe("Status:Work");
    expect(changed.events.some((e) => e.kind === "refused")).toBe(false);

    // A reload removes the profile, and there are several left: the window asks.
    kithd.profiles = ["Home", "Play"];
    kithd.remembered = "";
    await device();
    for (const s of [changed, unread]) {
      expect(s.events.at(-1)?.kind).toBe("refused");
    }
    await until(
      () => kithd.streams(backend.changed) + kithd.streams(backend.unreadStream) === 0,
      "both ended at kithd",
    );
    expect(await outcome(window, "Status", empty)).toBe("refused");

    // Switching profile ends the old one's streams too.
    await gate.call(window, "ChooseProfile", bytes(DeviceService.method.chooseProfile.input, { name: "Home" }));
    const home = listen("Changed");
    await until(() => kithd.streams(backend.changed) === 1, "Home's stream open");
    await gate.call(window, "ChooseProfile", bytes(DeviceService.method.chooseProfile.input, { name: "Play" }));
    expect(home.events.at(-1)?.kind).toBe("refused");
    await until(() => kithd.streams(backend.changed) === 0, "Home's stream ended at kithd");
    changed.stop();
    unread.stop();
    home.stop();
  });

  it("asks for the seat from this machine, and by force only once", async () => {
    await device();
    const seat = listen(
      "Seat",
      bytes(backend.seat.input, { client: "w", force: true, where: { pid: 7n, host: "elsewhere", sinceUnixMs: 5n } }),
    );
    const seats = () => kithd.received.filter((r) => r.method === "Seat").map((r) => r.message as SeatRequest);
    await until(() => seats().length === 1, "the seat asked for");
    await kithd.stop();
    await kithd.start();
    await until(() => seats().length === 2, "the seat asked for again on the new kithd");
    seat.stop();

    expect(seats().map((s) => s.force)).toEqual([true, false]);
    for (const s of seats()) {
      expect(s.client).toBe("w");
      expect(s.where?.pid).toBe(BigInt(process.pid));
      expect(s.where?.host).toBe(hostname());
      expect(s.where?.sinceUnixMs).toBe(5n);
    }
  });

  it("joins or leaves only a room the profile is invited to", async () => {
    await device();
    const leave = (roomId: string) => outcome(window, "LeaveRoom", bytes(backend.leaveRoom.input, { roomId }));
    const join = (roomIdOrAlias: string) =>
      outcome(window, "JoinRoom", bytes(backend.joinRoom.input, { roomIdOrAlias }));
    expect(await leave("!joined:x")).toBe("refused");
    expect(await leave("")).toBe("refused");
    expect(await join("#somewhere:x")).toBe("refused");
    expect(await join("!other:x")).toBe("refused");
    expect(kithd.reached.filter((r) => r.startsWith("LeaveRoom") || r.startsWith("JoinRoom"))).toEqual([]);
    expect(await leave("!in:x")).toBe("answered");
    expect(await join("!in:x")).toBe("answered");
    expect(kithd.reached.filter((r) => r.startsWith("LeaveRoom") || r.startsWith("JoinRoom"))).toEqual([
      "LeaveRoom:Work",
      "JoinRoom:Work",
    ]);
  });
});
