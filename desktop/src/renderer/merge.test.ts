import { describe, expect, it } from "vitest";

import table from "../../../internal/domain/testdata/timeline.json";
import { merge, replace } from "./merge";
import { message, type Row, row } from "./testing/rows";

interface Case {
  name: string;
  kind: string;
  shown: Row[];
  rows: Row[];
  want: Row[];
}

// The same table internal/domain's conformance test holds to MergeMessages.
describe("the timeline's order and merge, as Go's", () => {
  const cases = table.cases as Case[];
  it("has cases", () => {
    expect(cases.length).toBeGreaterThan(0);
  });
  for (const c of cases) {
    it(c.name, () => {
      const shown = c.shown.map((r) => message(r));
      const rows = c.rows.map((r) => message(r));
      let got;
      if (c.kind === "merge") {
        got = merge(shown, rows);
      } else {
        expect(rows).toHaveLength(1);
        got = replace(shown, rows[0] ?? message({ id: "" }));
      }
      expect(got.map(row)).toEqual(c.want);
    });
  }
});
