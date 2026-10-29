import { create, fromBinary } from "@bufbuild/protobuf";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { BackendService, Change, ChangedRequestSchema, ChangedResponseSchema } from "../gen/backend/v1/services_pb";
import type { StreamEvent } from "../shared/ipc";
import { backoff, keepOpen } from "./streams";
import { FakeKithd, until } from "./testing/kithd";

let kithd: FakeKithd;

beforeEach(async () => {
  kithd = new FakeKithd();
  await kithd.start();
});

afterEach(async () => {
  await kithd.dispose();
});

/** A stream kept open on kithd, with every event posted, stamped with when. */
function listen() {
  const events: (StreamEvent & { at: number })[] = [];
  const stop = keepOpen(
    kithd.transport(),
    BackendService.method.changed,
    create(ChangedRequestSchema),
    { "Kith-Profile": "Home" },
    (e) => events.push({ ...e, at: Date.now() }),
  );
  const changes = () =>
    events.flatMap((e) =>
      e.kind === "frames" ? e.frames.map((f) => fromBinary(ChangedResponseSchema, f).change) : [],
    );
  const count = (kind: StreamEvent["kind"]) => events.filter((e) => e.kind === kind).length;
  return { events, stop, changes, count };
}

describe("a stream main holds open", () => {
  it("posts every frame in order, a burst as a few messages", async () => {
    const burst = Array.from({ length: 200 }, (_, i) => (i % 2 ? Change.ROOMS : Change.ACCOUNTS));
    kithd.burst = burst;
    const s = listen();
    await until(() => s.changes().length === burst.length, "every frame");
    s.stop();
    expect(s.changes()).toEqual(burst);
    expect(s.count("frames")).toBeLessThan(20);
    expect(s.events[0]?.kind).toBe("attached");
  });

  it("posts what kithd sent before it went, then opens again on the kithd that comes back", async () => {
    const s = listen();
    await until(() => s.count("attached") === 1, "the stream to open");
    kithd.send(Change.ROOMS);
    kithd.send(Change.ACCOUNTS);
    await until(() => s.changes().length === 2, "the frames before the restart");

    await kithd.stop();
    await until(() => s.count("detached") >= 1, "the stream to end with kithd");
    await kithd.start();
    await until(() => s.count("attached") === 2, "the stream to open on the new kithd");
    kithd.send(Change.ROOMS);
    await until(() => s.changes().length === 3, "a frame from the new kithd");
    s.stop();

    expect(s.changes()).toEqual([Change.ROOMS, Change.ACCOUNTS, Change.ROOMS]);
    const kinds = s.events.map((e) => e.kind);
    // Every frame of an attachment is posted before the end that follows it.
    expect(kinds.indexOf("detached")).toBeGreaterThan(kinds.indexOf("frames"));
    expect(kinds.lastIndexOf("attached")).toBeGreaterThan(kinds.indexOf("detached"));
  });

  it("posts the frames of a stream that ended before the end", async () => {
    kithd.burst = [Change.ROOMS, Change.ACCOUNTS];
    kithd.endAtOnce = true;
    const s = listen();
    await until(() => s.count("detached") >= 1, "the stream to end");
    s.stop();
    expect(s.events.slice(0, 3).map((e) => e.kind)).toEqual(["attached", "frames", "detached"]);
    expect(s.changes().slice(0, 2)).toEqual([Change.ROOMS, Change.ACCOUNTS]);
  });

  it("asks a kithd that ends every stream at once less and less often", async () => {
    kithd.endAtOnce = true;
    const s = listen();
    await until(() => s.count("attached") >= 4, "four attempts", 10_000);
    s.stop();
    const opens = s.events.filter((e) => e.kind === "attached").map((e) => e.at);
    const gaps = opens.slice(1).map((at, i) => at - (opens[i] ?? at));
    expect(gaps[0]).toBeGreaterThanOrEqual(backoff.first - 10);
    for (let i = 1; i < gaps.length; i++) {
      expect(gaps[i], `gaps ${gaps.join(", ")}`).toBeGreaterThan((gaps[i - 1] ?? 0) * 1.5);
    }
  }, 15_000);

  it("posts nothing once stopped, and its request ends at kithd", async () => {
    const s = listen();
    await until(() => s.count("attached") === 1, "the stream to open");
    s.stop();
    await until(() => kithd.closed === 1, "kithd to see the stream end");
    const posted = s.events.length;
    kithd.send(Change.ROOMS);
    await new Promise((resolve) => setTimeout(resolve, backoff.first * 2));
    expect(s.events).toHaveLength(posted);
    expect(kithd.opened).toBe(1);
  });

  it("stops trying when stopped while kithd is away", async () => {
    await kithd.stop();
    const s = listen();
    await until(() => s.count("detached") === 2, "two failed attempts");
    s.stop();
    await new Promise((resolve) => setTimeout(resolve, backoff.first * 4));
    expect(s.count("detached")).toBe(2);
    expect(s.count("attached")).toBe(0);
  });
});
