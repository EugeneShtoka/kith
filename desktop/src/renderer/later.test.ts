// The composer's sending later and its commands, through the session, against the fake
// kithd and relay (testing/kithd.ts): kithd's queue and the commands folder.
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import fc from "fast-check";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ScheduledMessageSchema } from "../gen/backend/v1/services_pb";
import { DeviceSettingsSchema } from "../gen/device/v1/device_pb";
import { queueAfterDue, queueRefresh } from "./compose";
import { Session } from "./session";
import { Kithd, room, settled } from "./testing/kithd";
import { message } from "./testing/rows";

let kithd: Kithd;
let session: Session;

beforeEach(() => {
  kithd = new Kithd();
  vi.stubGlobal("window", { kith: kithd.bridge() });
  session = new Session();
});

afterEach(() => {
  session.stop();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

const composer = () => {
  const c = session.snapshot().composer;
  if (c === undefined) {
    throw new Error("no room is open");
  }
  return c;
};

/** flush lets every answer due arrive under fake timers, as settled does under real ones. */
async function flush(): Promise<void> {
  for (let i = 0; i < 25; i++) {
    await vi.advanceTimersByTimeAsync(1);
  }
}

async function opened(
  init: MessageInitShape<typeof DeviceSettingsSchema> = {},
  open = "!a",
  settle: () => Promise<void> = settled,
): Promise<void> {
  kithd.of("Home").rooms = [
    room("!a", { name: "Family", topic: "Sunday lunch, and who brings what" }),
    room("!sms", { sms: true }),
    room("!old", { replacement: "!a" }),
  ];
  kithd.settings = create(DeviceSettingsSchema, { markdown: true, confirmSms: true, emojiSet: "curated", ...init });
  session.start();
  await settle();
  session.open(open);
  await settle();
}

function typed(text: string): void {
  session.type(text, text.length);
}

/** sent is what a command or Send words became: what kithd sent (now, or from its queue). */
const sent = () =>
  kithd.sent.map((s) => `${s.draft.emote ? "* " : ""}${s.draft.body}${s.draft.plain ? " (plain)" : ""}`);

describe("sending later", () => {
  it("queues what /in and /at say, with its reply and mentions, and empties the composer as a send does", async () => {
    await opened();
    session.reply(message({ id: "$q", sender: "@bob:x", body: "lunch?" }, "!a"));
    typed("/in 2h see you then");
    const before = Date.now();
    await session.send();
    await settled();
    expect(kithd.queue).toHaveLength(1);
    const [q] = kithd.queue;
    expect([q?.roomId, q?.body, q?.replyTo]).toEqual(["!a", "see you then", "$q"]);
    expect(Number(q?.atMs) - before).toBeGreaterThanOrEqual(2 * 3600_000);
    expect(q?.txnId).toMatch(/^kith-/);
    expect(composer().draft).toMatchObject({ body: "", replyTo: "" });
    expect(kithd.drafts.has("!a")).toBe(false);
    expect(composer().note).toMatch(/^queued for .* — Family, as a reply$/);
    expect(sent()).toEqual([]);
  });

  it("queues the composer's words from the Later form, at a clock time or after a delay", async () => {
    await opened();
    typed("good morning");
    await session.later("at", "09:00");
    typed("and again");
    await session.later("in", "30m");
    await settled();
    expect(kithd.queue.map((q) => q.body)).toEqual(["good morning", "and again"]);
    expect(new Date(Number(kithd.queue[0]?.atMs)).getHours()).toBe(9);
    expect(composer().draft.body).toBe("");
  });

  it("says what is wrong with a time, keeping the words", async () => {
    await opened();
    typed("/in 2x hello");
    await session.send();
    expect(composer().note).toBe('/in "2x" is not a delay — try 30m, 2h, 1h30m');
    typed("/at 09:00");
    await session.send();
    expect(composer().note).toBe("/at needs a time and a message — try /at 09:00 good morning");
    expect(composer().draft.body).toBe("/at 09:00");
    typed("words");
    await session.later("at", "noon");
    expect(composer().note).toBe('/at "noon" is not a time — try 09:00 or 9pm');
    expect(composer().draft.body).toBe("words");
    expect(kithd.queue).toEqual([]);
  });

  it("asks before queueing to a chat that sends as SMS", async () => {
    await opened({}, "!sms");
    typed("/in 1h on my way");
    await session.send();
    expect(composer().asking?.room).toBe("!sms");
    expect(kithd.queue).toEqual([]);
    await session.answer(true);
    await settled();
    expect(kithd.queue.map((q) => q.body)).toEqual(["on my way"]);
  });

  it("queues a message once though its answer was lost: asked again under the transaction ID it had", async () => {
    await opened();
    kithd.scheduleFaults.push("after", "before");
    typed("/in 1h once");
    await session.send();
    await settled();
    expect(composer().unscheduled.map((u) => u.message.body)).toEqual(["once"]);
    expect(composer().draft.body).toBe(""); // gone from the composer as a send's words are
    const [u] = composer().unscheduled;
    if (u === undefined) {
      throw new Error("nothing unscheduled");
    }
    await session.scheduleAgain(u); // fails before kithd: still kept
    expect(composer().unscheduled).toHaveLength(1);
    await session.scheduleAgain(u);
    expect(composer().unscheduled).toEqual([]);
    expect(kithd.queue.map((q) => q.body)).toEqual(["once"]);
  });

  it("lists the queue as kithd holds it, read again as a message's time comes, and says when a cancel lost to the send", async () => {
    vi.useFakeTimers();
    await opened({}, "!a", flush);
    typed("/in 1m soon");
    void session.send();
    await flush();
    typed("/in 2h later");
    void session.send();
    await flush();
    session.showQueue(false);
    await flush();
    expect(composer().queue?.rows.map((r) => r.summary.split(" — ")[1])).toEqual(["soon", "later"]);
    const [soon, later] = kithd.queue;

    // Its time comes while the list is shown: kithd sends it, and the list is read again
    // just after (not at its next refresh), showing it gone.
    await vi.advanceTimersByTimeAsync(60_000);
    expect(composer().queue?.rows).toHaveLength(2);
    kithd.fire(soon?.id ?? "");
    await vi.advanceTimersByTimeAsync(queueAfterDue);
    await flush();
    expect(composer().queue?.rows.map((r) => r.id)).toEqual([later?.id]);

    // A cancel of the one that went out is not said to have canceled it, and sends nothing again.
    void session.cancelQueued(soon?.id ?? "");
    await flush();
    expect(composer().note).toBe("it was no longer waiting: it went out, or was canceled elsewhere");
    void session.cancelQueued(later?.id ?? "");
    await flush();
    expect(composer().note).toBe("canceled");
    expect(composer().queue?.rows).toEqual([]);
    expect(sent()).toEqual(["soon"]);

    // Every room's, each saying where it goes; and read again on its own while shown, so
    // one the TUI queued meanwhile shows.
    const at = BigInt(Date.now() + 3600_000);
    kithd.queue.push(create(ScheduledMessageSchema, { id: "tui1", roomId: "!sms", body: "elsewhere", atMs: at }));
    session.showQueue(true);
    await flush();
    expect(composer().queue?.rows.map((r) => [r.where, r.id])).toEqual([["!sms", "tui1"]]);
    kithd.queue.push(create(ScheduledMessageSchema, { id: "tui2", roomId: "!a", body: "more", atMs: at }));
    await vi.advanceTimersByTimeAsync(queueRefresh);
    await flush();
    expect(composer().queue?.rows.map((r) => r.id)).toEqual(["tui1", "tui2"]);
    session.closeQueue();
    expect(composer().queue).toBeUndefined();
  });
});

describe("the composer's commands", () => {
  it("send /me as an emote and /plain as typed, and `//` as one slash", async () => {
    await opened();
    for (const words of ["/me waves", "/plain *as typed*", "//me is not a command"]) {
      typed(words);
      await session.send();
    }
    await settled();
    expect(sent()).toEqual(["* waves", "*as typed* (plain)", "/me is not a command"]);
    expect(composer().draft.body).toBe("");
  });

  it("say so of one the window has not got, or one missing its argument, keeping the words", async () => {
    await opened();
    typed("/summary 2h");
    await session.send();
    expect(composer().note).toBe("/summary is in the TUI; it is not in the window yet");
    expect(composer().draft.body).toBe("/summary 2h");
    typed("/me");
    await session.send();
    expect(composer().note).toBe("/me <what you are doing>");
    expect(sent()).toEqual([]);
    // A correction's words are the message: a slash in them is no command.
    session.edit(message({ id: "$m", sender: "@me:x", body: "/me waved" }, "!a"));
    typed("/me waves");
    await session.send();
    await settled();
    expect(kithd.sent.map((s) => [s.draft.body, s.draft.edits, s.draft.emote])).toEqual([["/me waves", "$m", false]]);
  });

  it("do what the window does: the queue, the topic, the room that replaced this one, a poll", async () => {
    await opened();
    typed("/scheduled");
    await session.send();
    await settled();
    expect(composer().queue?.room).toBe("!a");
    expect(composer().draft.body).toBe("");
    typed("/topic");
    await session.send();
    expect(composer().reading).toEqual({
      title: "Family",
      body: "Sunday lunch, and who brings what",
      truncated: false,
    });
    typed("/poll Lunch? | soup | tacos | any");
    await session.send();
    await settled();
    expect(kithd.writes.filter((w) => w.startsWith("CreatePoll"))).toHaveLength(1);
    session.open("!old");
    await settled();
    typed("/replacement");
    await session.send();
    await settled();
    expect(session.snapshot().open).toBe("!a");
  });

  it("run the commands folder's by name in the relay, its output where its block says, and give it no words", async () => {
    await opened();
    kithd.userCommands = [
      { name: "zoom", applies: true },
      { name: "post", applies: true },
      { name: "standup", applies: false, places: ["space:Work"] },
      { name: "open", applies: true },
    ];
    kithd.ranWith.set("zoom", { output: "compose", body: "https://meet.example/abc" });
    kithd.ranWith.set("post", { output: "send" });
    kithd.ranWith.set("open", { links: ["https://a.example", "https://b.example"] });
    session.wantCommands();
    await settled();
    expect(composer().commands.map((c) => c.name)).toEqual(["zoom", "post", "standup", "open"]);

    session.reply(message({ id: "$q", sender: "@bob:x", body: "a link?" }, "!a"));
    typed("/zoom");
    await session.send();
    expect(composer().draft.body).toBe("https://meet.example/abc");
    expect(composer().note).toBe("/zoom — enter to send");
    typed("/post");
    await session.send();
    expect(composer().draft).toMatchObject({ body: "", replyTo: "" });
    typed("/zoom tomorrow"); // words after it stay in the window
    await session.send();
    expect(composer().note).toBe("/zoom: the window runs your commands with no words after them");
    typed("/standup");
    await session.send();
    expect(composer().note).toBe("/standup is only for space:Work");
    expect(composer().draft.body).toBe("/standup"); // refused, never sent as words
    typed("/open");
    await session.send();
    expect(composer().choosing?.links).toEqual(["https://a.example", "https://b.example"]);
    await session.chooseLink(1);
    expect(kithd.runs).toEqual(["zoom !a $q 0:Home", "post !a $q 0:Home", "open !a  0:Home", "open !a  2:Home"]);
    expect(sent()).toEqual([]);

    // A slash word no command has is a message, as the TUI sends it.
    typed("/shrug ok");
    await session.send();
    await settled();
    expect(sent()).toEqual(["/shrug ok"]);
  });
});

/** A step of the walk: the person writing, sending and sending later, kithd's faults, its queue sending. */
type Step =
  | { kind: "type" | "send" | "later" | "slash" | "again" | "fire" | "assistant" | "pause" | "hold" }
  | { kind: "fault"; when: "before" | "after" }
  | { kind: "deliver"; i: number };

const step: fc.Arbitrary<Step> = fc.oneof(
  { weight: 6, arbitrary: fc.constant({ kind: "type" as const }) },
  { weight: 2, arbitrary: fc.constant({ kind: "send" as const }) },
  { weight: 2, arbitrary: fc.constant({ kind: "later" as const }) },
  { weight: 2, arbitrary: fc.constant({ kind: "slash" as const }) },
  { weight: 2, arbitrary: fc.constant({ kind: "again" as const }) },
  { weight: 2, arbitrary: fc.constant({ kind: "fire" as const }) },
  { weight: 2, arbitrary: fc.constant({ kind: "assistant" as const }) },
  { weight: 2, arbitrary: fc.constant({ kind: "pause" as const }) },
  { weight: 2, arbitrary: fc.constant({ kind: "hold" as const }) },
  {
    weight: 2,
    arbitrary: fc.record({ kind: fc.constant("fault" as const), when: fc.constantFrom("before", "after") }),
  },
  { weight: 2, arbitrary: fc.record({ kind: fc.constant("deliver" as const), i: fc.nat(4) }) },
);

describe("sending later, whatever the interleaving", () => {
  // The person writes and sends, or sends later (the Later form, or /in), while kith-mcp
  // appends to the draft, kithd's Schedule answers late or fails before or after it
  // queued (the answer lost), the window queues again what was not queued, and kithd's
  // queue sends what came due. At rest every word written is, exactly once, in a message
  // sent or in the room's draft: a message queued instead of sent leaves the composer as a
  // send does, and one asked for twice is queued once.
  it("keeps every word once", async () => {
    await fc.assert(
      fc.asyncProperty(fc.array(step, { minLength: 1, maxLength: 30 }), async (steps) => {
        session.stop();
        kithd = new Kithd();
        vi.stubGlobal("window", { kith: kithd.bridge() });
        session = new Session();
        vi.useFakeTimers();
        await opened({}, "!a", flush);
        const words: string[] = [];
        let n = 0;
        const body = () => composer().draft.body;
        for (const s of steps) {
          switch (s.kind) {
            case "type": {
              const w = `w${String(++n)}`;
              words.push(w);
              typed(`${body()} ${w}`.trim());
              break;
            }
            case "send":
              void session.send();
              break;
            case "later":
              void session.later("in", "1h");
              break;
            case "slash":
              if (body() !== "") {
                typed(`/in 1h ${body()}`);
                void session.send();
              }
              break;
            case "again": {
              const [u] = composer().unscheduled;
              if (u !== undefined) {
                void session.scheduleAgain(u);
              }
              break;
            }
            case "fire":
              kithd.fire(kithd.queue[0]?.id ?? "");
              break;
            case "assistant": {
              const w = `a${String(++n)}`;
              if (kithd.append("!a", w)) {
                words.push(w);
              }
              break;
            }
            case "pause":
              session.watch(true);
              session.watch(false);
              break;
            case "hold":
              if (!kithd.holding.delete("Schedule")) {
                kithd.holding.add("Schedule");
              }
              break;
            case "fault":
              kithd.scheduleFaults.push(s.when);
              break;
            case "deliver":
              kithd.releaseAt(s.i);
              break;
          }
          await flush();
        }
        // At rest: kithd answers everything, the window queues again what it kept, the
        // queue sends, and the drafts are written.
        kithd.holding.clear();
        kithd.scheduleFaults.length = 0;
        kithd.release();
        await flush();
        for (let round = 0; round < 5 && composer().unscheduled.length > 0; round++) {
          for (const u of composer().unscheduled) {
            void session.scheduleAgain(u);
          }
          await flush();
        }
        expect(composer().unscheduled).toEqual([]);
        for (const q of [...kithd.queue]) {
          kithd.fire(q.id);
        }
        session.watch(true);
        session.watch(false);
        await flush();
        vi.useRealTimers();
        const where = [...kithd.sent.map((x) => x.draft.body), kithd.drafts.get("!a")?.body ?? ""];
        const count = new Map<string, number>();
        for (const w of where.join(" ").split(/\s+/)) {
          count.set(w, (count.get(w) ?? 0) + 1);
        }
        for (const w of words) {
          expect(count.get(w) ?? 0, `${w} in ${JSON.stringify(where)}`).toBe(1);
        }
      }),
      { numRuns: 120 },
    );
  }, 30_000); // 120 runs take ~5 s alone, more beside the Go tests in make quick
});
