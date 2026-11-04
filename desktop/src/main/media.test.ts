import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { mediaURL } from "../shared/media";
import { type Frame, Gate } from "./gate";
import { byteRange, Media } from "./media";
import { FakeKithd } from "./testing/kithd";

const window: Frame = { url: "app://kith/index.html", parent: null };

let kithd: FakeKithd;
let gate: Gate;
let loads: number;
let media: Media;

beforeEach(async () => {
  kithd = new FakeKithd();
  await kithd.start();
  gate = new Gate(kithd.transport());
  loads = 0;
  media = new Media((room, event) => {
    loads++;
    return gate.media(room, event);
  });
});

afterEach(async () => {
  await kithd.dispose();
});

const bytes = async (url: string) => new Uint8Array(await (await media.answer(url)).arrayBuffer());

describe("kith-media:", () => {
  it("answers a message's attachment from kithd, once, in the window's profile", async () => {
    await gate.call(window, "Device", new Uint8Array());
    kithd.pictures.set("$p:x", new Uint8Array([1, 2, 3]));
    const url = mediaURL("!r:x", "$p:x", "v1");
    expect(await bytes(url)).toEqual(new Uint8Array([1, 2, 3]));
    expect(await bytes(url)).toEqual(new Uint8Array([1, 2, 3]));
    expect(loads).toBe(1);
    expect(kithd.reached).toEqual(["Device:", "LoadImage:Home"]);
  });

  it("holds the content in its key: a picture an edit replaced is never the old bytes", async () => {
    await gate.call(window, "Device", new Uint8Array());
    kithd.pictures.set("$p:x", new Uint8Array([1]));
    expect(await bytes(mediaURL("!r:x", "$p:x", "first"))).toEqual(new Uint8Array([1]));
    kithd.pictures.set("$p:x", new Uint8Array([2])); // an edit replaced it
    expect(await bytes(mediaURL("!r:x", "$p:x", "edited"))).toEqual(new Uint8Array([2]));
  });

  it("answers nothing before a profile, for an attachment kithd lacks, or for another URL", async () => {
    expect((await media.answer(mediaURL("!r:x", "$p:x", ""))).status).toBe(502); // no profile yet
    await gate.call(window, "Device", new Uint8Array());
    expect((await media.answer(mediaURL("!r:x", "$none:x", ""))).status).toBe(502);
    for (const url of [
      "kith-media://other/a/b",
      "kith-media://media/a",
      "kith-media://media/a/b/c",
      "kith-media://media//b",
      "kith-media://media/%E0%A4%A/b",
      "app://kith/index.html",
      "not a url",
    ]) {
      expect((await media.answer(url)).status, url).toBe(404);
    }
    expect(kithd.reached.filter((r) => r.startsWith("LoadImage"))).toEqual(["LoadImage:Home"]);
  });

  it("answers the part a player asks for, so a voice note or a video can seek", async () => {
    await gate.call(window, "Device", new Uint8Array());
    kithd.pictures.set("$v:x", new Uint8Array([0, 1, 2, 3, 4, 5, 6, 7, 8, 9]));
    const url = mediaURL("!r:x", "$v:x", "v1");
    const part = await media.answer(url, "bytes=2-4");
    expect(part.status).toBe(206);
    expect(part.headers.get("Content-Range")).toBe("bytes 2-4/10");
    expect(part.headers.get("Content-Length")).toBe("3");
    expect(new Uint8Array(await part.arrayBuffer())).toEqual(new Uint8Array([2, 3, 4]));
    const past = await media.answer(url, "bytes=10-");
    expect([past.status, past.headers.get("Content-Range")]).toEqual([416, "bytes */10"]);
    const whole = await media.answer(url);
    expect([whole.headers.get("Accept-Ranges"), whole.headers.get("Content-Length")]).toEqual(["bytes", "10"]);
    expect(loads).toBe(1); // every part from the bytes kept
  });
});

describe("a Range header", () => {
  const size = 10;
  for (const [header, want] of [
    ["bytes=0-", [0, 9]],
    ["bytes=3-5", [3, 5]],
    ["bytes=3-99", [3, 9]], // past the end: to the end
    ["bytes=-4", [6, 9]], // the last four
    ["bytes=-99", [0, 9]],
    ["bytes=9-9", [9, 9]],
    ["bytes=10-", null], // none of them
    ["bytes=5-3", null],
    ["bytes=-0", null],
    ["bytes=0-1,4-5", undefined], // several: answered whole
    ["items=0-1", undefined],
    ["bytes=-", undefined],
  ] as const) {
    it(`${header} asks for ${JSON.stringify(want)}`, () => {
      expect(byteRange(header, size)).toEqual(want);
    });
  }
});
