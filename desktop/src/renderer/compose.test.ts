import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { type Message, PollSchema, ReactionSchema } from "../gen/backend/v1/types_pb";
import { DeviceSettingsSchema } from "../gen/device/v1/device_pb";
import { typingRefresh } from "./compose";
import { saveDelay } from "./drafts";
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

const view = () => session.snapshot();
const composer = () => {
  const c = view().composer;
  if (c === undefined) {
    throw new Error("no room is open");
  }
  return c;
};

/** opened is the window seated in Home with !a open, the device settings as given. */
/** settings are the device's, each key its default but those given. */
function settings(init: MessageInitShape<typeof DeviceSettingsSchema>) {
  return create(DeviceSettingsSchema, {
    sendReceipts: true,
    unreadLine: true,
    markdown: true,
    confirmSms: true,
    sendTyping: true,
    emojiSet: "curated",
    reactionsScope: "room",
    ...init,
  });
}

async function opened(init: MessageInitShape<typeof DeviceSettingsSchema> = {}, open = "!a"): Promise<void> {
  kithd.of("Home").rooms = [room("!a"), room("!sms", { sms: true })];
  kithd.settings = settings(init);
  session.start();
  await settled();
  session.open(open);
  await settled();
}

/** typed is the person typing text into the open composer. */
function typed(text: string): void {
  session.type(text, text.length);
}

/** saved writes the drafts now, as the window going out of sight does. */
async function saved(): Promise<void> {
  session.watch(true);
  session.watch(false);
  await settled();
}

const bodies = () => kithd.sent.map((s) => s.draft.body);

describe("the composer", () => {
  it("sends the words with their Markdown rendered, or as typed where [composer] markdown is off", async () => {
    await opened();
    typed("**hi**");
    await session.send();
    await settled();
    expect(kithd.sent.map((s) => [s.room, s.draft.body, s.draft.plain])).toEqual([["!a", "**hi**", false]]);
    expect(composer().draft.body).toBe("");
    expect(kithd.drafts.has("!a")).toBe(false);

    kithd.settings = settings({ markdown: false });
    session.open("!sms");
    session.open("!a"); // the settings are read as a room opens
    await settled();
    typed("*as typed*");
    await session.send();
    expect(kithd.sent.at(-1)?.draft.plain).toBe(true);
  });

  it("replies to a message, and corrects one of this person's: the words before it come back after", async () => {
    await opened();
    const m = message({ id: "$m", sender: "@me:x", body: "teh typo" }, "!a");
    typed("half a thought");
    session.reply(m);
    expect(composer().draft.replyTo).toBe("$m");
    session.edit(m);
    expect(composer().draft).toMatchObject({ body: "teh typo", editing: "$m", editSaved: "half a thought" });
    typed("the typo");
    await session.send();
    await settled();
    expect(kithd.sent.map((s) => [s.draft.body, s.draft.edits, s.draft.replyTo])).toEqual([["the typo", "$m", ""]]);
    expect(composer().draft).toMatchObject({ body: "half a thought", editing: "", replyTo: "$m" });
    session.cancel();
    expect(composer().draft.replyTo).toBe("");
  });

  it("asks before sending to a chat that sends as SMS, and keeps the words when told not to", async () => {
    await opened({}, "!sms");
    typed("on my way");
    await session.send();
    expect(composer().asking).toEqual({ room: "!sms", files: 0 });
    expect(kithd.sent).toEqual([]);
    await session.answer(false);
    expect(composer().asking).toBeUndefined();
    expect(composer().draft.body).toBe("on my way");
    expect(kithd.sent).toEqual([]);
    await session.send();
    await session.answer(true);
    await settled();
    expect(bodies()).toEqual(["on my way"]);
  });

  it("sends to such a chat unasked where [composer] confirm_sms is off", async () => {
    await opened({ confirmSms: false }, "!sms");
    typed("on my way");
    await session.send();
    await settled();
    expect(bodies()).toEqual(["on my way"]);
  });

  it("sends a message once though its answer was lost: retried under the transaction ID it had", async () => {
    await opened();
    kithd.loseSends = 1;
    typed("hello");
    await session.send();
    await settled();
    expect(composer().unsent.map((u) => u.draft.body)).toEqual(["hello"]);
    const [unsent] = composer().unsent;
    if (unsent === undefined) {
      throw new Error("no failed send");
    }
    await session.retry(unsent);
    expect(composer().unsent).toEqual([]);
    expect(bodies()).toEqual(["hello"]);
    expect(kithd.calls.filter((c) => c.startsWith("Send:"))).toHaveLength(2);
  });

  it("sends while a save is on the wire: the words sent never come back, and the assistant's stay", async () => {
    await opened();
    kithd.holding.add("ReplaceDraft");
    typed("hi");
    await saved(); // written; its answer on its way
    kithd.append("!a", "the report"); // the assistant, meanwhile
    await session.send();
    await settled();
    kithd.holding.clear();
    kithd.release();
    await settled();
    expect(bodies()).toEqual(["hi"]);
    expect(kithd.drafts.get("!a")?.body).toBe("the report");
    expect(composer().draft.body).toBe("the report");
  });

  it("tells the room while the person types, again every ten seconds, and that they stopped as it is sent", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    await opened();
    const typing = () => kithd.writes.filter((w) => w.startsWith("SendTyping"));
    typed("h");
    typed("hi");
    await settled();
    expect(typing()).toEqual(["SendTyping !a true"]);
    vi.setSystemTime(Date.now() + typingRefresh);
    typed("hi there");
    await session.send();
    await settled();
    expect(typing()).toEqual(["SendTyping !a true", "SendTyping !a true", "SendTyping !a false"]);
  });

  it("tells no room where [display] send_typing is off", async () => {
    await opened({ sendTyping: false });
    typed("hi");
    await settled();
    expect(kithd.writes.filter((w) => w.startsWith("SendTyping"))).toEqual([]);
  });

  it("toggles a reaction: sent, or taken back where it is this person's", async () => {
    kithd.of("Home").reactions = [
      create(ReactionSchema, { id: "$mine", roomId: "!a", target: "$m", key: "👍", sender: "@me:x" }),
      create(ReactionSchema, { id: "$theirs", roomId: "!a", target: "$m", key: "🎉", sender: "@bob:x" }),
    ];
    await opened();
    const m = message({ id: "$m", sender: "@bob:x", body: "news" }, "!a");
    await session.react(m, "👍");
    await session.react(m, "🎉");
    expect(kithd.writes.filter((w) => !w.startsWith("RecordEmoji"))).toEqual([
      "Redact !a $mine",
      "SendReaction !a $m 🎉",
    ]);
  });

  it("sends the files offered, the words as the first one's caption", async () => {
    await opened();
    await session.pick();
    await session.offer([new File(["x"], "photo.png")]);
    expect(composer().offered.map((o) => o.name)).toEqual(["report.pdf", "photo.png"]);
    typed("both");
    await session.send();
    await settled();
    expect(kithd.attached).toEqual(["!a report.pdf both", "!a photo.png "]);
    expect(composer().offered).toEqual([]);
    expect(composer().draft.body).toBe("");
  });

  it("steps aside for another window: its drafts written from the seat first, then nothing more", async () => {
    await opened();
    typed("unsaved words");
    kithd.otherTakesOver("Home");
    await settled();
    expect(view().seat.state).toBe("steppedAside");
    expect(kithd.seats.get("Home")).toBe("other");
    expect(kithd.drafts.get("!a")?.body).toBe("unsaved words");
  });
});

describe("a poll", () => {
  /** asking is a message of Bob's in !a asking a poll, this person's answers mine. */
  const asking = (mine: string[] = [], init: { multiple?: boolean; closed?: boolean } = {}): Message => {
    const m = message({ id: "$p", sender: "@bob:x", body: "📊 Lunch? (Soup / Tacos)" }, "!a");
    m.poll = create(PollSchema, {
      question: "Lunch?",
      ...init,
      options: [
        { id: "s", text: "Soup", votes: 1n, mine: mine.includes("s") },
        { id: "t", text: "Tacos", votes: 0n, mine: mine.includes("t") },
      ],
    });
    return m;
  };
  const votes = () => kithd.writes.filter((w) => w.startsWith("VotePoll"));

  it("is voted in, by one answer or several, and the vote taken back by none", async () => {
    await opened();
    await session.vote(asking(), ["t"]);
    await session.vote(asking([], { multiple: true }), ["s", "t"]);
    await session.vote(asking(["s"]), []);
    expect(votes()).toEqual(["VotePoll !a $p t", "VotePoll !a $p s,t", "VotePoll !a $p "]);
    expect(view().problem).toBeUndefined();
  });

  it("closed, takes no vote, and says so", async () => {
    await opened();
    await session.vote(asking([], { closed: true }), ["s"]);
    expect(votes()).toEqual([]);
    expect(view().problem).toBe("this poll is closed");
  });

  it("shows who voted once asked, and a vote puts it away", async () => {
    kithd.pollVoters.set("$p", { voters: [{ id: "@me:x", options: ["s"] }] });
    await opened();
    const m = asking(["s"]);
    session.showVoters(m);
    expect(composer().voters).toMatchObject({ event: "$p", read: false });
    await settled();
    expect(composer().voters?.answer?.voters.map((v) => v.id)).toEqual(["@me:x"]);
    await session.vote(m, ["t"]);
    expect(composer().voters).toBeUndefined();
  });

  it("drops who voted read for a poll put away, or shown again since", async () => {
    kithd.pollVoters.set("$p", { voters: [{ id: "@bob:x", options: ["s"] }] });
    await opened();
    const m = asking();
    kithd.holding.add("PollVoters");
    session.showVoters(m);
    await settled();
    session.closeVoters();
    kithd.release();
    await settled();
    expect(composer().voters).toBeUndefined();

    session.showVoters(m); // held: the first answer
    await settled();
    kithd.holding.delete("PollVoters");
    kithd.pollVoters.set("$p", { voters: [] });
    session.showVoters(m); // shown again: its own answer only
    await settled();
    expect(composer().voters?.answer?.voters).toEqual([]);
    kithd.release();
    await settled();
    expect(composer().voters?.answer?.voters).toEqual([]);
  });

  it("says why who voted could not be read", async () => {
    await opened();
    session.showVoters(asking());
    await settled();
    expect(composer().voters).toMatchObject({ read: true, error: "matrix: $p asks no poll" });
  });

  it("is asked in the open room, and where its network asks none it says so", async () => {
    kithd.noPolls.add("!sms");
    await opened();
    expect(await session.askPoll({ question: "Lunch?", options: ["Soup", "Tacos"], multiple: true })).toBe(true);
    expect(kithd.writes.filter((w) => w.startsWith("CreatePoll"))).toEqual(["CreatePoll !a Lunch? Soup|Tacos true"]);
    session.open("!sms");
    await settled();
    expect(await session.askPoll({ question: "Lunch?", options: ["Soup", "Tacos"], multiple: false })).toBe(false);
    expect(view().problem).toBe(
      "could not create the poll: daemon: that room's network cannot do this: creating polls in whatsapp rooms",
    );
  });
});

// The window leaves a profile (a switch), or reloads, or closes, just after the person
// typed: kithd takes a room's draft only in a profile holding the room (#444), and a
// reload leaves no time to write. Home holds !h and !s, Work holds !w and !s. Time is
// fake: the debounce before a save, and the wait for the drafts, pass as the test says.
describe("a draft as the window leaves a profile", () => {
  const holds = new Map<string, readonly string[]>([
    ["Home", ["!h", "!s"]],
    ["Work", ["!w", "!s"]],
  ]);
  const pass = async (ms: number) => {
    await vi.advanceTimersByTimeAsync(ms);
  };
  /** drive waits for p, the time passing in steps: a switch waits for the drafts a while. */
  const drive = async (p: Promise<void>) => {
    const state = { done: false };
    void p.finally(() => {
      state.done = true;
    });
    for (let i = 0; i < 600 && !state.done; i++) {
      await pass(10);
    }
    await p;
  };
  const body = () => view().composer?.draft.body ?? "";
  const write = (word: string) => {
    const text = `${body()} ${word}`.trim();
    session.type(text, text.length);
  };
  const stored = (room: string) => kithd.drafts.get(room)?.body ?? "";
  const start = async () => {
    vi.useFakeTimers();
    kithd.profiles = ["Home", "Work"];
    kithd.remembered = "Home";
    kithd.of("Home").rooms = [room("!h"), room("!s")];
    kithd.of("Work").rooms = [room("!w"), room("!s")];
    session.start();
    await pass(100);
  };
  /** reload is the window loading again: the old one goes as a page does, a new one starts. */
  const reload = async () => {
    kithd.reloadWindow();
    session.stop();
    session = new Session();
    session.start();
    await pass(100);
  };
  const open = async (room: string) => {
    session.open(room);
    await pass(50);
  };

  it("is written before the switch, in the profile it was typed in", async () => {
    await start();
    await open("!h");
    write("w1");
    await drive(session.choose("Work"));
    expect(view().profile).toBe("Work");
    expect(stored("!h")).toBe("w1");
    expect(kithd.calls).toContain("ReplaceDraft:Home");
    expect(kithd.calls).not.toContain("ReplaceDraft:Work");
  });

  it("is written once more where its write fails, before the switch", async () => {
    await start();
    await open("!h");
    write("w1");
    kithd.draftFaults.push("before");
    await drive(session.choose("Work"));
    expect(stored("!h")).toBe("w1");
    expect(view().problem).toBeUndefined();
  });

  it("failing twice, is said not saved, kept, and written once its room is open again", async () => {
    await start();
    await open("!h");
    write("w1");
    kithd.draftFaults.push("before", "before");
    await drive(session.choose("Work"));
    expect(view().profile).toBe("Work");
    expect(stored("!h")).toBe("");
    expect(view().problem).toMatch(/draft in !h is not saved/);
    await drive(session.choose("Home"));
    await open("!h");
    expect(body()).toBe("w1");
    expect(view().problem).toBeUndefined(); // back in its composer, it is written as typed
    await pass(saveDelay + 10);
    expect(stored("!h")).toBe("w1");
  });

  it("does not hold the switch for a write kithd does not answer, and does not write it twice", async () => {
    await start();
    await open("!h");
    write("w1");
    kithd.holding.add("ReplaceDraft");
    await pass(saveDelay + 10); // on the wire, unanswered
    const switched = session.choose("Work");
    await pass(2100);
    await drive(switched);
    expect(view().profile).toBe("Work");
    kithd.holding.clear();
    kithd.release(); // it had landed: only the answer was lost
    await pass(50);
    await drive(session.choose("Home"));
    await open("!h");
    expect(body()).toBe("w1");
    await pass(saveDelay + 10);
    expect(stored("!h")).toBe("w1");
  });

  it("in a room both profiles hold, is kept for the next and written there", async () => {
    await start();
    await open("!s");
    write("w1");
    kithd.draftFaults.push("before", "before");
    await drive(session.choose("Work"));
    await open("!s");
    expect(body()).toBe("w1");
    await pass(saveDelay + 10);
    expect(stored("!s")).toBe("w1");
    expect(kithd.calls).toContain("ReplaceDraft:Work");
  });

  it("is written once by two switches in a row, there and back", async () => {
    await start();
    await open("!h");
    write("w1");
    const there = session.choose("Work");
    const back = session.choose("Home");
    await drive(there);
    await drive(back);
    expect(view().profile).toBe("Home");
    await open("!h");
    expect(body()).toBe("w1");
    await pass(saveDelay + 10);
    expect(stored("!h")).toBe("w1");
  });

  it("stepping aside for another window, failing twice, is said kept", async () => {
    await start();
    await open("!h");
    write("w1");
    kithd.draftFaults.push("before", "before");
    kithd.otherTakesOver("Home");
    await pass(3000);
    expect(view().seat.state).toBe("steppedAside");
    expect(view().problem).toMatch(/draft in !h is not saved/);
  });

  it("is kept through a reload, and written once its room is open again", async () => {
    await start();
    await open("!h");
    write("w1");
    await reload();
    expect(stored("!h")).toBe("");
    expect(view().problem).toMatch(/draft in !h is not saved/);
    await open("!h");
    expect(body()).toBe("w1");
    await pass(saveDelay + 10);
    expect(stored("!h")).toBe("w1");
  });

  it("is written as the window closes, a failing write once more", async () => {
    await start();
    await open("!h");
    write("w1");
    kithd.draftFaults.push("before");
    await drive(kithd.closeWindow());
    expect(stored("!h")).toBe("w1");
  });

  // The person types, pauses, opens rooms, switches (two at once too) and reloads, while
  // kithd fails writes and holds their answers: at rest, with every room opened again in
  // a profile holding it, every word typed is stored once.
  it.each(Array.from({ length: 30 }, (_, i) => i + 1))("keeps every word over any switches (seed %i)", async (seed) => {
    let s = seed;
    const pick = (n: number) => {
      s = (s * 1103515245 + 12345) % 2 ** 31;
      return Math.floor((s / 2 ** 31) * n);
    };
    await start();
    const typed = new Map<string, string[]>();
    const switches: Promise<void>[] = [];
    let n = 0;
    for (let step = 0; step < 40; step++) {
      switch (pick(10)) {
        case 0:
        case 1: {
          const rooms = holds.get(view().profile) ?? [];
          const r = rooms[pick(rooms.length)];
          if (r !== undefined) {
            await open(r);
          }
          break;
        }
        case 2:
        case 3:
          if (view().open !== "" && view().composer !== undefined) {
            const word = `w${String(++n)}`;
            typed.set(view().open, [...(typed.get(view().open) ?? []), word]);
            write(word);
          }
          break;
        case 4:
          await pass(pick(2) === 0 ? 300 : saveDelay + 10);
          break;
        case 5:
          if (pick(2) === 0) {
            kithd.holding.add("ReplaceDraft");
          } else {
            kithd.holding.clear();
            kithd.release();
          }
          break;
        case 6:
          kithd.draftFaults.push("before");
          break;
        case 7:
        case 8:
          switches.push(session.choose(view().profile === "Home" ? "Work" : "Home"));
          break;
        default:
          await Promise.all(switches.splice(0).map(drive)); // a reload ends the page, switches with it
          await reload();
      }
      await pass(1);
    }
    kithd.holding.clear();
    kithd.draftFaults.length = 0;
    kithd.release();
    await Promise.all(switches.splice(0).map(drive));
    for (const [profile, rooms] of holds) {
      if (view().profile !== profile) {
        await drive(session.choose(profile));
      }
      for (const r of rooms) {
        await open(r);
        await pass(saveDelay + 10);
      }
    }
    await pass(3000);
    for (const [r, words] of typed) {
      const there = stored(r).split(/\s+/);
      for (const word of words) {
        expect(
          there.filter((w) => w === word),
          `${word} in ${r}: ${JSON.stringify(stored(r))}, seed ${String(seed)}`,
        ).toHaveLength(1);
      }
    }
  });
});
