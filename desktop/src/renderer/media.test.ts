import { describe, expect, it } from "vitest";

import { mediaOf } from "../shared/media";
import { attachmentURL } from "./media";
import { message } from "./testing/rows";

describe("an attachment's URL", () => {
  const first = message({ id: "$p:x", media: "cat.png" }, "!r:x");

  it("names its room and event, however they are spelled", () => {
    const odd = message({ id: "$a/b?c#d%", media: "x" }, "matrix:abc/!r:x.org");
    expect(mediaOf(attachmentURL(odd))).toEqual({ room: "matrix:abc/!r:x.org", event: "$a/b?c#d%" });
  });

  it("changes when an edit replaces the picture, or the picture's description changes", () => {
    const edited = message({ id: "$p:x", media: "cat.png", edited: true, editedAt: 5, revisionId: "$e1" }, "!r:x");
    const renamed = message({ id: "$p:x", media: "dog.png" }, "!r:x");
    const urls = [first, edited, renamed].map(attachmentURL);
    expect(new Set(urls).size).toBe(3);
    expect(attachmentURL(message({ id: "$p:x", media: "cat.png" }, "!r:x"))).toBe(urls[0]);
  });
});
