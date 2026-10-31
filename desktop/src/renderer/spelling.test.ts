import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { DeviceSettingsSchema } from "../gen/device/v1/device_pb";
import { assistDebounce } from "./complete";
import { Session } from "./session";
import { spellDebounce } from "./spell";
import { Kithd, room, settled } from "./testing/kithd";

// The composer's spelling and completion, through the window's session, as kithd answers.

let kithd: Kithd;
let session: Session;

beforeEach(() => {
  kithd = new Kithd();
  vi.stubGlobal("window", { kith: kithd.bridge() });
  session = new Session();
  kithd.misspelled.set("wrld", ["world", "weld"]);
  kithd.misspelled.set("teh", ["the"]);
});

afterEach(() => {
  session.stop();
  vi.unstubAllGlobals();
});

const composer = () => {
  const c = session.snapshot().composer;
  if (c === undefined) {
    throw new Error("no room is open");
  }
  return c;
};
const marked = () => composer().spell.marks.map((m) => m.word);

/** opened is !a open, with the device's settings [spell] and [complete] as given. */
async function opened(init: MessageInitShape<typeof DeviceSettingsSchema> = {}): Promise<void> {
  kithd.of("Home").rooms = [room("!a"), room("!b")];
  kithd.settings = create(DeviceSettingsSchema, {
    markdown: true,
    emojiSet: "curated",
    reactionsScope: "room",
    spellUnderline: "curly",
    rareUnderline: "dotted",
    autocorrect: "off",
    complete: true,
    completeGhost: true,
    completeRatio: 3n,
    completeScope: "room",
    ...init,
  });
  session.start();
  await settled();
  session.open("!a");
  await settled();
}

/** paused is a pause in typing long enough for a check and a lookup to be answered. */
async function paused(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, Math.max(spellDebounce, assistDebounce) + 20));
  await settled();
}

const typed = (text: string) => session.type(text, text.length);

describe("spelling in the composer", () => {
  it("marks what kithd rejects after a pause, and moves the marks with what is typed before them", async () => {
    await opened();
    typed("hello wrld ");
    await paused();
    expect(marked()).toEqual(["wrld"]);
    session.type("oh hello wrld ", 2); // typed at the start
    expect(composer().spell.marks.map((m) => [m.word, m.start])).toEqual([["wrld", 9]]);
    await paused();
    expect(kithd.calls.filter((c) => c.startsWith("CheckSpelling"))).toHaveLength(2);
  });

  it("puts right a word as it is finished, with autocorrect on and one certain fix", async () => {
    await opened({ autocorrect: "misspellings" });
    typed("teh");
    typed("teh ");
    await settled(); // asked at once, no pause
    expect(composer().draft.body).toBe("the ");
    typed("the wrld ");
    await settled();
    expect(composer().draft.body).toBe("the wrld "); // two fixes: never unasked
  });

  it("walks the marks: a suggestion taken for every copy, a word added to the dictionary", async () => {
    await opened();
    typed("teh wrld teh ");
    await paused();
    session.openWalk();
    expect(composer().spell.walk?.items.map((m) => m.word)).toEqual(["teh", "wrld", "teh"]);
    expect(session.walkTake(0)).toBe(12); // after the last copy put right
    expect(composer().draft.body).toBe("the wrld the ");
    expect(composer().spell.walk?.items[composer().spell.walk?.at ?? 0]?.word).toBe("wrld");
    session.walkLearn(true);
    await settled();
    expect(kithd.learned).toEqual(["LearnWord wrld forever"]);
    expect(composer().spell.walk).toBeUndefined(); // the end: nothing left
    expect(marked()).toEqual([]);
  });

  it("asks before a send with a word marked ([spell] check_before_send), and sends once through the walk", async () => {
    await opened({ checkBeforeSend: true });
    typed("hello wrld");
    await session.send(); // not checked yet: checked now
    await settled();
    expect(kithd.sent).toEqual([]);
    expect(composer().spell.walk?.sendWhenDone).toBe(true);
    await session.walkSkipAll();
    await settled();
    expect(kithd.sent.map((s) => s.draft.body)).toEqual(["hello wrld"]);
  });

  it("does not send a draft changed while it was checked before sending", async () => {
    await opened({ checkBeforeSend: true });
    typed("hello wrld");
    kithd.holding.add("CheckSpelling");
    const sending = session.send();
    await settled();
    typed("hello wrld, and more");
    kithd.release();
    await sending;
    await settled();
    expect(kithd.sent).toEqual([]);
    expect(composer().note).toBe("the draft changed while it was being checked — send again");
  });

  it("asks no more once kithd has nothing to check with", async () => {
    kithd.spellOff = true;
    await opened();
    typed("hello wrld");
    await paused();
    typed("hello wrld again");
    await paused();
    expect(kithd.calls.filter((c) => c.startsWith("CheckSpelling"))).toHaveLength(1);
    session.openWalk();
    expect(composer().note).toBe("nothing to check spelling with");
  });

  it("drops what was asked about another room's draft", async () => {
    await opened();
    typed("wrld");
    kithd.holding.add("CheckSpelling");
    await paused();
    session.open("!b");
    await settled();
    session.open("!a");
    kithd.holding.delete("CheckSpelling");
    kithd.release(); // the answer about !a's draft before !b was opened
    await settled();
    expect(marked()).toEqual([]);
    await paused(); // !a's draft is checked again
    expect(marked()).toEqual(["wrld"]);
  });
});

describe("completion in the composer", () => {
  it("draws the word that clearly leads, and Tab takes it with a space", async () => {
    kithd.vocabulary.set("tomorrow", 40).set("tomato", 2);
    await opened();
    typed("see you tomo");
    await paused();
    expect(composer().assist).toMatchObject({ ghost: "rrow", from: "word" });
    expect(session.complete(true)).toBe(17);
    expect(composer().draft.body).toBe("see you tomorrow ");
  });

  it("asks the completion model at a word's end, unless it was turned down", async () => {
    kithd.model = { text: "the office", options: ["the office", "noon"], model: "local" };
    await opened();
    typed("see you at ");
    await settled();
    expect(composer().assist).toMatchObject({ ghost: "the office", from: "model", marker: "local" });
    expect(session.complete(false)).toBe(15); // right: one word of it
    expect(composer().draft.body).toBe("see you at the ");
    session.stop();

    kithd = new Kithd();
    vi.stubGlobal("window", { kith: kithd.bridge() });
    session = new Session();
    await opened({ modelDeclined: true });
    typed("see you at ");
    await settled();
    expect(kithd.calls.filter((c) => c.startsWith("ModelTask"))).toEqual([]);
  });

  it("asks nothing with [complete] off", async () => {
    kithd.vocabulary.set("tomorrow", 40);
    await opened({ complete: false });
    typed("see you tomo");
    await paused();
    expect(composer().assist.ghost).toBe("");
    expect(kithd.calls.filter((c) => c.startsWith("CompleteWord"))).toEqual([]);
  });
});

describe("the one-time offers", () => {
  it("offers the dictionaries, word counts and model in turn, installing what is ticked and turning down the rest", async () => {
    kithd.languages = {
      candidates: [
        { tag: "he_IL", script: "Hebrew", share: 0.4, bytes: 1n << 20n },
        { tag: "de_DE", script: "Latin", share: 0.1, bytes: 1n << 20n },
        { tag: "fr_FR", script: "Latin", share: 0.1, bytes: 1n << 20n },
      ],
      frequencies: [{ tag: "he_IL", script: "Hebrew", recommended: true }],
    };
    kithd.modelOffer = { offer: true, candidate: { tag: "m1", name: "Model", recall: 0.5, bytes: 1n << 28n } };
    await opened({ spellDeclined: ["fr_FR"] });
    await settled();
    const offer = () => session.snapshot().offers.prompt;
    expect(offer()?.rows.map((r) => [r.tag, r.ticked])).toEqual([
      ["he_IL", true],
      ["de_DE", true],
    ]);
    session.acceptOffer(["he_IL"]);
    await settled();
    expect(kithd.installed).toEqual(["InstallDictionary he_IL"]);
    expect(kithd.settings !== "fail" && kithd.settings.spellDeclined).toEqual(["fr_FR", "de_DE"]);
    expect(offer()?.kind).toBe("frequencies");
    session.acceptOffer([]);
    await settled();
    expect(kithd.settings !== "fail" && kithd.settings.frequenciesDeclined).toEqual(["he_IL"]);
    expect(offer()?.kind).toBe("model");
    session.dismissOffer(); // not now: nothing written
    await settled();
    expect(offer()).toBeUndefined();
    expect(kithd.settings !== "fail" && kithd.settings.modelDeclined).toBe(false);
    expect(session.snapshot().offers.note).toBe("installed he_IL — spellcheck is ready");
  });
});
