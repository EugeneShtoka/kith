import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { type Message, PollSchema, ReactionSchema } from "../gen/backend/v1/types_pb";
import { DeviceSettingsSchema } from "../gen/device/v1/device_pb";
import { typingRefresh } from "./compose";
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
