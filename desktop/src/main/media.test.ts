import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { mediaURL } from "../shared/media";
import { type Frame, Gate } from "./gate";
import { Media } from "./media";
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
});
