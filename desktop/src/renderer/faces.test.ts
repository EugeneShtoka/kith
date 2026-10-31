import fc from "fast-check";
import { describe, expect, it } from "vitest";

import { hueOf, hues, initials, networkMark } from "./faces";

describe("faces", () => {
  it("give one ID one color, always in the palette", () => {
    fc.assert(
      fc.property(fc.string(), (id) => {
        const h = hueOf(id);
        expect(Number.isInteger(h) && h >= 0 && h < hues).toBe(true);
        expect(hueOf(id)).toBe(h);
      }),
    );
    // Not all one color: a room of a few people tells them apart.
    const ids = ["@ann:x", "@bob:x", "@cy:x", "@di:x", "@ed:x", "@flo:x"];
    expect(new Set(ids.map(hueOf)).size).toBeGreaterThan(2);
  });

  it("draw a name as the initials of its first and last words", () => {
    expect(initials("Ada Lovelace")).toBe("AL");
    expect(initials("ada byron lovelace")).toBe("AL");
    expect(initials("Ada")).toBe("A");
    expect(initials("@ada:example.org")).toBe("A");
    expect(initials("  (Work) chat ")).toBe("WC");
    expect(initials("Ölga Ünal")).toBe("ÖÜ");
    expect(initials("🎉 Party")).toBe("P");
    expect(initials("Лев Толстой")).toBe("ЛТ");
  });

  it("draw a name with no letter as # and no name as ?", () => {
    expect(initials("+1 555 0100")).toBe("#");
    expect(initials("🎉")).toBe("#");
    expect(initials("")).toBe("?");
    expect(initials("   ")).toBe("?");
  });

  it("mark a room by its network as the wire names it, and an unnamed one not at all", () => {
    expect(networkMark("WhatsApp")).toEqual({ abbr: "WA", slug: "whatsapp" });
    expect(networkMark("Google Messages")).toEqual({ abbr: "SMS", slug: "gmessages" });
    expect(networkMark("Mastodon")).toEqual({ abbr: "MA", slug: "other" });
    expect(networkMark("")).toBeUndefined();
  });
});
