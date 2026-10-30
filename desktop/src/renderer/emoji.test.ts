import { describe, expect, it } from "vitest";

import {
  completions,
  defaultReactions,
  emojiSet,
  finished,
  fold,
  noScores,
  palette,
  shortcodeAt,
  shortcodeOf,
  toned,
} from "./emoji";

const medium = "\u{1F3FD}";

describe("emoji", () => {
  it("offer each tier's vocabulary, curated names and the person's winning", () => {
    const curated = emojiSet("curated", { shipit: "🐿️", thumbsup: "🦫" });
    expect(curated.byName.get("+1")).toBe("👍");
    expect(curated.byName.get("grinning_face")).toBeUndefined();
    expect(curated.byName.get("thumbsup")).toBe("🦫");
    expect(curated.byName.get("shipit")).toBe("🐿️");
    expect(emojiSet("standard", {}).byName.get("grinning_face")).toBe("😀");
    expect(emojiSet("standard", {}).byName.get("face_in_clouds")).toBeUndefined();
    expect(emojiSet("complete", {}).byName.get("face_in_clouds")).toBe("😶‍🌫️");
    // Each emoji once, named by its shortest name.
    expect(new Set(curated.all).size).toBe(curated.all.length);
    expect(shortcodeOf(curated, "👍")).toBe("+1");
    expect(shortcodeOf(curated, `👍${medium}`)).toBe("+1");
  });

  it("tone what takes a tone, and nothing else", () => {
    expect(toned("👍", medium)).toBe(`👍${medium}`);
    expect(toned("👍\u{1F3FB}", medium)).toBe(`👍${medium}`);
    expect(toned("❤️", medium)).toBe("❤️");
    expect(toned("👍", "")).toBe("👍");
  });

  it("rank the quick palette by the room's reactions, then the static palette, toned", () => {
    const set = emojiSet("curated", {});
    expect(palette(set, noScores, [], "")).toEqual(defaultReactions);
    const scores = { reaction: fold({ "🔥": 30, "👍\u{1F3FB}": 5, "👍": 5 }), composed: fold({ "🦀": 99 }) };
    const got = palette(set, scores, [], medium);
    expect(got.slice(0, 3)).toEqual(["🔥", `👍${medium}`, "🦀"]);
    expect(got).toHaveLength(10);
    expect(palette(set, noScores, ["🎉", "x"], "")[0]).toBe("🎉");
  });

  it("complete a :shortcode: being typed, and put the emoji for one closed", () => {
    expect(shortcodeAt("hi :sm", 6)).toEqual({ start: 3, query: "sm" });
    expect(shortcodeAt("hi :s", 5)).toBeUndefined();
    expect(shortcodeAt("10:30", 5)).toBeUndefined();
    const set = emojiSet("curated", {});
    expect(completions(set, "thumbs", noScores, [])).toEqual(["👎", "👍"]); // by name
    expect(completions(set, "thumbs", noScores, defaultReactions)).toEqual(["👍", "👎"]);
    expect(finished(set, "ok :+1: then", 7, medium)).toEqual({
      text: `ok 👍${medium} then`,
      caret: 7,
      emoji: `👍${medium}`,
    });
    expect(finished(set, "at 10:30:", 9, "")).toBeUndefined();
    expect(finished(set, "a :nonesuch:", 12, "")).toBeUndefined();
  });
});
