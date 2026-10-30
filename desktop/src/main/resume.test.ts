import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { BackendService, type MessagesRequest, MessagesResponseSchema } from "../gen/backend/v1/services_pb";
import { MessageSchema } from "../gen/backend/v1/types_pb";
import type { StreamEvent } from "../shared/ipc";
import { type Frame, Gate } from "./gate";
import { FakeKithd, until } from "./testing/kithd";

const window: Frame = { url: "app://kith/index.html", parent: null };
const backend = BackendService.method;
const empty = new Uint8Array();

let kithd: FakeKithd;
let gate: Gate;

beforeEach(async () => {
  kithd = new FakeKithd();
  await kithd.start();
  gate = new Gate(kithd.transport());
  await gate.call(window, "Device", empty);
});

afterEach(async () => {
  await kithd.dispose();
});

const commit = (n: number) => {
  for (let i = 0; i < n; i++) {
    kithd.commit(create(MessageSchema, { id: `$${String(kithd.changes.length + 1)}`, roomId: "!r" }));
  }
};

/** listen opens the Messages stream as the window does, keeping what main posts. */
function listen() {
  const events: StreamEvent[] = [];
  const stop = gate.subscribe(
    window,
    "Messages",
    toBinary(backend.messages.input, create(backend.messages.input)),
    (e) => events.push(e),
  );
  /** posted is what the window was given: each message's ID, and "reload" where it was told to read again. */
  const posted = () =>
    events.flatMap((e) =>
      e.kind === "reload"
        ? ["reload"]
        : e.kind === "frames"
          ? e.frames.flatMap((f) => {
              const id = fromBinary(MessagesResponseSchema, f).message?.id;
              return id === undefined ? [] : [id];
            })
          : [],
    );
  const count = (kind: StreamEvent["kind"]) => events.filter((e) => e.kind === kind).length;
  return { events, stop, posted, count };
}

const asked = () => kithd.received.filter((r) => r.method === "Messages").map((r) => r.message as MessagesRequest);
const ids = (from: number, to: number) => Array.from({ length: to - from + 1 }, (_, i) => `$${String(from + i)}`);

describe("the Messages stream main holds", () => {
  it("resumes after the last change it posted when kithd restarts mid-burst: none lost, none twice", async () => {
    commit(5); // before the window: not its to see
    const s = listen();
    await until(() => s.count("attached") === 1 && kithd.streams(backend.messages) === 1, "the stream open");
    commit(10);
    await until(() => s.posted().length === 10, "the first changes posted");
    commit(3000);
    await new Promise((resolve) => setTimeout(resolve, 2)); // a little of the burst read
    await kithd.stop(); // the rest cut off: written, never read
    await until(() => s.count("detached") === 1, "the stream to end");
    const before = s.posted().length;
    expect(before).toBeLessThan(3010); // the premise: cut off mid-burst
    commit(50); // while kithd is away, the cache still changes (another writer)
    await kithd.start();
    await until(() => s.posted().length === 3060, "the rest of the burst", 10_000);
    s.stop();

    expect(s.posted()).toEqual(ids(6, 3065));
    const [first, again] = asked();
    expect(first?.sinceEpoch).toBe("");
    expect(again?.sinceEpoch).toBe("e1");
    expect(again?.sinceChange).toBe(BigInt(5 + before));
  });

  it("posts a read_again as a reload in its place, then goes on from where it said", async () => {
    const s = listen();
    await until(() => kithd.streams(backend.messages) === 1, "the stream open");
    commit(2);
    await until(() => s.posted().length === 2, "the first changes");
    await kithd.stop();
    kithd.epoch = "e2"; // the cache was rebuilt: its numbers mean nothing now
    kithd.changes.length = 0;
    await kithd.start();
    await until(() => s.posted().includes("reload") && kithd.streams(backend.messages) === 1, "the reload");
    commit(1);
    await until(() => s.posted().length === 4, "the change after it");
    await kithd.stop();
    await kithd.start();
    await until(() => asked().length === 3, "the stream opened a third time");
    s.stop();

    expect(s.posted()).toEqual(["$1", "$2", "reload", "$1"]);
    expect(asked().map((a) => [a.sinceEpoch, a.sinceChange])).toEqual([
      ["", 0n],
      ["e1", 2n],
      ["e2", 1n],
    ]);
  });

  it("starts afresh, from the newest, for a window loading afresh", async () => {
    const s = listen();
    await until(() => kithd.streams(backend.messages) === 1, "the stream open");
    commit(3);
    await until(() => s.posted().length === 3, "the changes");
    gate.restart(); // the window reloads: the stream it held ends
    await until(() => kithd.streams(backend.messages) === 0, "the old stream ended");
    await gate.call(window, "Device", empty);
    const next = listen();
    await until(() => kithd.streams(backend.messages) === 1, "the new stream open");
    next.stop();
    s.stop();
    expect(asked().map((a) => a.sinceEpoch)).toEqual(["", ""]);
  });
});
