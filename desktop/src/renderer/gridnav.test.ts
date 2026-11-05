import { describe, expect, it } from "vitest";

import { arrowTo } from "./gridnav";

describe("arrows over a grid", () => {
  // Seven buttons in rows of three: 0 1 2 / 3 4 5 / 6.
  it("move by one across rows, by a row up and down, and stop at the ends", () => {
    expect(arrowTo("ArrowRight", 2, 7, 3)).toBe(3);
    expect(arrowTo("ArrowRight", 6, 7, 3)).toBe(6);
    expect(arrowTo("ArrowLeft", 3, 7, 3)).toBe(2);
    expect(arrowTo("ArrowLeft", 0, 7, 3)).toBe(0);
    expect(arrowTo("ArrowDown", 1, 7, 3)).toBe(4);
    expect(arrowTo("ArrowDown", 5, 7, 3)).toBe(6); // to the shorter last row's last
    expect(arrowTo("ArrowDown", 6, 7, 3)).toBe(6);
    expect(arrowTo("ArrowUp", 4, 7, 3)).toBe(1);
    expect(arrowTo("ArrowUp", 1, 7, 3)).toBe(-2); // above the first row: the field
    expect(arrowTo("Home", 5, 7, 3)).toBe(0);
    expect(arrowTo("End", 0, 7, 3)).toBe(6);
    expect(arrowTo("Enter", 0, 7, 3)).toBeUndefined();
  });

  it("move along one row, which up and down leave alone", () => {
    expect(arrowTo("ArrowRight", 0, 5, 5)).toBe(1);
    expect(arrowTo("ArrowDown", 2, 5, 5)).toBe(2);
    expect(arrowTo("ArrowUp", 2, 5, 5)).toBe(-3);
  });
});
