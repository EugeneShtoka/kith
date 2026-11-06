import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import { MessageSchema } from "../gen/backend/v1/types_pb";
import { openable } from "../shared/ipc";
import { links } from "./links";

const message = (body: string, hrefs: string[] = []) =>
  create(MessageSchema, { body, format: { text: body, spans: hrefs.map((href) => ({ link: true, href })) } });

describe("a message's links, as domain.LinksWithHrefs finds them", () => {
  it.each([
    ["none", "nothing here", []],
    ["one, in the words", "see https://example.org/a now", ["https://example.org/a"]],
    [
      "each once, in order",
      "http://b.example x https://a.example http://b.example",
      ["http://b.example", "https://a.example"],
    ],
    ["less the sentence's punctuation", "go to https://example.org/a.", ["https://example.org/a"]],
    ["less quotes and colons after", 'it said "https://example.org/q":', ["https://example.org/q"]],
    [
      "keeping a bracket it opens",
      "https://en.wikipedia.org/wiki/Go_(game)",
      ["https://en.wikipedia.org/wiki/Go_(game)"],
    ],
    ["less a bracket around it", "(https://example.org/p)", ["https://example.org/p"]],
    ["ending at markup", "<https://example.org/m>|label", ["https://example.org/m"]],
  ])("%s", (_, body, want) => {
    expect(links(message(body))).toEqual(want);
  });

  it("adds the formatted links' web targets after the words', then their chat links, leaving out others", () => {
    expect(
      links(
        message("https://a.example and x/y", [
          "https://x.example/y",
          "https://a.example",
          "matrix:r/room:x",
          "mailto:a@b",
        ]),
      ),
    ).toEqual(["https://a.example", "https://x.example/y", "matrix:r/room:x"]);
  });
});

describe("a message's chat links that are no web links, as domain.MessageLinks finds them", () => {
  it("adds them after the web links, from the words and then the formatted links, each once", () => {
    expect(
      links(
        message(
          "go (matrix:r/team:x), tg://resolve?domain=kith or https://t.me/kith; whatsapp://send?phone=15551234567 and file:///x",
          ["matrix:u/dana:x", "matrix:r/team:x", "mailto:a@b"],
        ),
      ),
    ).toEqual([
      "https://t.me/kith",
      "matrix:r/team:x",
      "tg://resolve?domain=kith",
      "whatsapp://send?phone=15551234567",
      "matrix:u/dana:x",
    ]);
  });
});

describe("what the browser may be handed", () => {
  it.each([
    ["https://example.org", true],
    ["HTTP://example.org", true],
    ["file:///etc/passwd", false],
    ["javascript:alert(1)", false],
    ["matrix:r/room:x", false],
  ])("%s: %s", (link, want) => {
    expect(openable(link)).toBe(want);
  });
});
