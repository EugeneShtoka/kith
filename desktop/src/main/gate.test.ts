import { create, toBinary } from "@bufbuild/protobuf";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { BackendService, ChangedRequestSchema, StatusRequestSchema } from "../gen/backend/v1/services_pb";
import { DeviceService } from "../gen/device/v1/device_pb";
import type { StreamEvent } from "../shared/ipc";
import { type Frame, Gate, Refused } from "./gate";
import { FakeKithd, until } from "./testing/kithd";

const window: Frame = { url: "app://kith/index.html", parent: null };
const status = toBinary(StatusRequestSchema, create(StatusRequestSchema));
const changed = toBinary(ChangedRequestSchema, create(ChangedRequestSchema));

let kithd: FakeKithd;
let gate: Gate;

beforeEach(async () => {
  kithd = new FakeKithd();
  await kithd.start();
  gate = new Gate(kithd.transport(), { "Kith-Profile": "Work" });
});

afterEach(async () => {
  await kithd.dispose();
});

/** outcome is what the gate did with a call: answered, or refused before kithd. */
async function outcome(frame: Frame | null, name: unknown, request: unknown): Promise<"answered" | "refused"> {
  try {
    await gate.call(frame, name, request);
    return "answered";
  } catch (err) {
    if (err instanceof Refused) {
      return "refused";
    }
    throw err;
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
    for (const name of names) {
      const request = name === "Device" ? new Uint8Array() : status;
      const want = name === "Status" || name === "Device" ? "answered" : "refused";
      expect(await outcome(window, name, request), String(name)).toBe(want);
    }
    expect(kithd.received.map((r) => r.method)).toEqual(["Status", "Device"]);
  });

  it("opens only the streams on the list, and refuses every other name before kithd", () => {
    for (const name of names) {
      expect(opened(window, name, changed), String(name)).toBe(name === "Changed" ? "opened" : "refused");
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
    for (const [frame, allowed] of frames) {
      const what = JSON.stringify(frame);
      expect(await outcome(frame, "Status", status), what).toBe(allowed ? "answered" : "refused");
      expect(opened(frame, "Changed", changed), what).toBe(allowed ? "opened" : "refused");
    }
    expect(kithd.received.filter((r) => r.method === "Status")).toHaveLength(frames.filter(([, ok]) => ok).length);
  });

  it("refuses a request that is not its method's, and what reaches kithd is only the method's own fields", async () => {
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

  it("sends the device's profile with every call and stream", async () => {
    await gate.call(window, "Status", status);
    await gate.call(window, "Device", new Uint8Array());
    const events: StreamEvent[] = [];
    const stop = gate.subscribe(window, "Changed", changed, (e) => events.push(e));
    await until(() => events.some((e) => e.kind === "attached"), "the stream to attach");
    stop();
    expect(kithd.received.map((r) => `${r.method}:${String(r.profile)}`)).toEqual([
      "Status:Work",
      "Device:Work",
      "Changed:Work",
    ]);
  });
});
