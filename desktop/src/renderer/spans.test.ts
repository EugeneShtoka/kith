import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import table from "../../../internal/richtext/testdata/spans.json";
import { SpanSchema } from "../gen/backend/v1/types_pb";
import { utf16At } from "./spans";

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
