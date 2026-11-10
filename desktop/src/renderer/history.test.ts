import { create } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";
import { describe, expect, it } from "vitest";

import { DeletionSchema } from "../gen/backend/v1/services_pb";
import { RevisionSchema } from "../gen/backend/v1/types_pb";
import { changeNote, deleted, noVersions, versions } from "./history";
import { message } from "./testing/rows";

const rev = (id: string, body: string, ms: number) => create(RevisionSchema, { id, body, at: timestampFromMs(ms) });

describe("a message's history", () => {
  it("lists each version oldest first, each edit with how much it changed", () => {
    const m = message({ id: "$m", ts: 1000, body: "hello there", edited: true });
    const listed = versions(m, [rev("$m", "helo", 1000), rev("$e1", "hello", 2000), rev("$e2", "hi", 3000)]);
    expect(listed.map((v) => [v.at?.getTime(), v.what, v.body])).toEqual([
      [1000, "sent", "helo"],
      [2000, "edited +1", "hello"],
      [3000, "edited −3", "hi"],
    ]);
  });

  it("is a message's own words where none was kept and it was never edited: a deleted one's, kept", () => {
    const kept = message({ id: "$m", ts: 1000, body: "gone now", redacted: true });
    expect(versions(kept, []).map((v) => [v.what, v.body])).toEqual([["sent", "gone now"]]);
    // Edited before its versions were kept: its words now are not what it was sent as.
    const edited = message({ id: "$m", body: "later words", edited: true });
    expect(versions(edited, [])).toEqual([]);
    expect(noVersions(edited)).toMatch(/edited before/);
    // Deleted, its words not kept.
    const erased = message({ id: "$m", redacted: true });
    expect(versions(erased, [])).toEqual([]);
    expect(noVersions(erased)).toMatch(/not kept/);
  });

  it("says who deleted it, why and when: the history's word over the row's, the sender's own unnamed", () => {
    const m = message({
      id: "$m",
      sender: "@a",
      redacted: true,
      redactedBy: "@mod",
      redactedReason: "spam",
      redactedAt: 5000,
    });
    expect(deleted(m, undefined)).toEqual({ by: "@mod", reason: "spam", at: new Date(5000) });
    const told = create(DeletionSchema, { by: "@b", reason: "", atUnixMs: 7000n });
    expect(deleted(m, told)).toEqual({ by: "@b", reason: "", at: new Date(7000) });
    expect(deleted({ ...m, redactedBy: "@a", redactedReason: "" }, undefined)?.by).toBe("");
    expect(deleted({ ...m, redacted: false }, told)).toBeUndefined();
  });

  it("counts an emoji of several code points as one character", () => {
    expect(changeNote("hi", "hi 👍🏽")).toBe(" +2");
    expect(changeNote("same", "s4me")).toBe(" reworded");
  });
});
