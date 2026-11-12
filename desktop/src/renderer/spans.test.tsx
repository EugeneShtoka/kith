import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import table from "../../../internal/richtext/testdata/spans.json";
import { renderToStaticMarkup } from "react-dom/server";

import { FormattedSchema, MentionSchema, SpanSchema } from "../gen/backend/v1/types_pb";
import { FormattedText, pills, utf16At } from "./spans";

// The same table internal/richtext's conformance test holds to Parse: each span's byte
// offsets, turned into UTF-16 indices, cut the same text Go's slice does.
describe("span offsets, from UTF-8 bytes to UTF-16 indices", () => {
  it("has cases", () => {
    expect(table.cases.length).toBeGreaterThan(0);
  });
  for (const c of table.cases) {
    it(c.name, () => {
      const at = utf16At(c.text);
      const spans = c.spans.map((s) => create(SpanSchema, { ...s, start: BigInt(s.start), end: BigInt(s.end) }));
      expect(spans.map((s) => c.text.slice(at(s.start), at(s.end)))).toEqual(c.pieces);
    });
  }

  it("moves an offset inside a character to its end, and clamps one outside the text", () => {
    const text = "a😀b";
    const at = utf16At(text);
    expect([0n, 1n, 2n, 3n, 4n, 5n, 6n, 99n, -1n].map(at)).toEqual([0, 1, 3, 3, 3, 3, 4, 4, 0]);
  });
});

describe("mentions", () => {
  const mention = (userId: string, name: string, roomId = "", knownAs = "") =>
    create(MentionSchema, { userId, name, roomId, knownAs });
  const classOf = (person: string) => `of-${person}`;

  it("fall where the text names each person, the longest name first, never over another", () => {
    const text = "@Dana and @Dan met @Dana";
    const got = pills(text, [mention("dan", "@Dan"), mention("dana", "@Dana"), mention("", "@room", "!r")], classOf);
    expect(got).toEqual([
      { start: 0, end: 5, className: "of-dana", name: "" },
      { start: 10, end: 14, className: "of-dan", name: "" },
    ]);
    expect(pills("nobody here", [mention("eli", "@Eli")], classOf)).toEqual([]);
  });

  it("are drawn in their person's color without the @, the rest of the text as it is", () => {
    const f = create(FormattedSchema, {
      text: "hi @Dana!",
      spans: [create(SpanSchema, { start: 0n, end: 6n, bold: true })], // over "hi @Da"
    });
    const html = renderToStaticMarkup(
      <FormattedText f={f} pills={pills(f.text, [mention("dana", "@Dana")], classOf)} />,
    );
    expect(html).toBe(
      '<span><strong>hi </strong></span><span class="named of-dana"><span><strong>Da</strong></span></span><span class="named of-dana">na</span>!',
    );
  });

  it("are drawn by kithd's name for the person, once, whatever the text calls them", () => {
    // Svetlana's Telegram account calls itself "neke"; kithd names her by her alias.
    const f = create(FormattedSchema, {
      text: "hi @neke!",
      spans: [create(SpanSchema, { start: 0n, end: 6n, bold: true })], // over "hi @ne"
    });
    const html = renderToStaticMarkup(
      <FormattedText f={f} pills={pills(f.text, [mention("telegram:66919467", "@neke", "", "Svetlana")], classOf)} />,
    );
    expect(html).toBe(
      '<span><strong>hi </strong></span><span class="named of-telegram:66919467"><span><strong>Svetlana</strong></span></span>!',
    );
  });
});
