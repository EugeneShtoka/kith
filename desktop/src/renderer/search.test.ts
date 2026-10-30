import { describe, expect, it } from "vitest";

import table from "../../../internal/domain/testdata/search.json";
import { emptyFilter, parseSearch } from "./search";

interface Case {
  input: string;
  terms: string;
  sender?: string;
  senderField?: string;
  since?: string;
  until?: string;
  empty?: boolean;
}

/** wall is a time as the table writes it: a wall clock in the window's zone ("" none). */
function wall(at: Date | undefined): string {
  if (at === undefined) {
    return "";
  }
  const two = (n: number) => String(n).padStart(2, "0");
  return (
    `${String(at.getFullYear())}-${two(at.getMonth() + 1)}-${two(at.getDate())}` +
    `T${two(at.getHours())}:${two(at.getMinutes())}:${two(at.getSeconds())}`
  );
}

// The same table internal/domain's conformance test holds to ParseSearch, read in this
// zone as Go reads it in its own.
describe("a typed search, as Go parses it", () => {
  const cases = table.cases as Case[];
  const now = new Date(table.now); // no offset: the wall clock here
  it("has cases", () => {
    expect(cases.length).toBeGreaterThan(0);
  });
  for (const c of cases) {
    it(JSON.stringify(c.input), () => {
      const got = parseSearch(c.input, now);
      expect({
        terms: got.terms,
        sender: got.sender,
        senderField: got.senderField,
        since: wall(got.since),
        until: wall(got.until),
      }).toEqual({
        terms: c.terms,
        sender: c.sender ?? "",
        senderField: c.senderField ?? "",
        since: c.since ?? "",
        until: c.until ?? "",
      });
      if (c.empty !== undefined) {
        expect(emptyFilter(got)).toBe(c.empty);
      }
    });
  }
});
