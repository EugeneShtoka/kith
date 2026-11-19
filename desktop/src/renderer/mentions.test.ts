import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import { MemberSchema } from "../gen/backend/v1/types_pb";
import { matching, mentionAt, nameOf, rankMembers, senderOf } from "./mentions";

const m = (userId: string, displayName = "") => create(MemberSchema, { userId, displayName });

describe("mentions", () => {
  it("rank recent speakers, then the most mentioned, then the rest, each once and members only", () => {
    const all = [m("@a:x", "Ann"), m("@b:x", "Bob"), m("@c:x", "Cy"), m("@d:x", "Di")];
    const got = rankMembers(all, ["@c:x", "@gone:x", "@c:x"], ["@b:x", "@c:x"], 0).map((x) => x.userId);
    expect(got).toEqual(["@c:x", "@b:x", "@a:x", "@d:x"]);
    expect(rankMembers(all, [], [], 2)).toHaveLength(2);
  });

  it("offer people for an @ being typed, by name or ID", () => {
    expect(mentionAt("hi @bo", 6)).toEqual({ start: 3, query: "bo" });
    expect(mentionAt("@", 1)).toEqual({ start: 0, query: "" });
    expect(mentionAt("mail a@b", 8)).toBeUndefined();
    const ranked = [m("@bob:x", "Bob"), m("@robert:x", "Rob"), m("@ann:x", "Ann")];
    expect(matching(ranked, "ob").map((x) => x.userId)).toEqual(["@bob:x", "@robert:x"]);
    expect(matching(ranked, "ann:").map((x) => x.userId)).toEqual(["@ann:x"]);
  });

  it("name a person as the room's name rule shows them, else as kithd names them", () => {
    const dana = create(MemberSchema, { userId: "@d:x", displayName: "+1555", knownAs: "Dana Levi", shownAs: "Dana" });
    expect(nameOf(dana)).toBe("Dana");
    expect(nameOf(create(MemberSchema, { userId: "@d:x", knownAs: "Dana Levi" }))).toBe("Dana Levi");
    expect(matching([dana], "dana l").map((p) => p.userId)).toEqual(["@d:x"]);
    const said = { sender: "@d:x", senderName: "+1555", senderKnownAs: "Dana Levi", senderShownAs: "Dana" };
    expect(senderOf(said)).toBe("Dana");
    expect(senderOf({ ...said, senderShownAs: "" })).toBe("Dana Levi");
  });
});
