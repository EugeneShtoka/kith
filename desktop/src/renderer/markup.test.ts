import { describe, expect, it } from "vitest";

import { marked } from "./markup";

describe("the formatting bar", () => {
  it("wraps the selection and leaves the caret after it", () => {
    expect(marked("say hi now", 4, 6, "bold")).toEqual({ text: "say **hi** now", caret: 10 });
    expect(marked("say hi now", 4, 6, "italic")).toEqual({ text: "say *hi* now", caret: 8 });
    expect(marked("run ls", 4, 6, "code")).toEqual({ text: "run `ls`", caret: 8 });
  });

  it("puts empty marks at the caret, it between them", () => {
    expect(marked("ab", 1, 1, "bold")).toEqual({ text: "a****b", caret: 3 });
    expect(marked("", 0, 0, "code")).toEqual({ text: "``", caret: 1 });
  });

  it("links the selected words, the caret where the address goes", () => {
    expect(marked("see docs", 4, 8, "link")).toEqual({ text: "see [docs]()", caret: 11 });
    expect(marked("see ", 4, 4, "link")).toEqual({ text: "see []()", caret: 5 });
    expect(marked("at https://kith.example/a", 3, 25, "link")).toEqual({
      text: "at [](https://kith.example/a)",
      caret: 4,
    });
  });
});
