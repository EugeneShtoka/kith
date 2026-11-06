import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import table from "../../../internal/domain/testdata/read.json";
import { DeviceSettingsSchema } from "../gen/device/v1/device_pb";
import { announces, readAfter } from "./read";

interface Case {
  name: string;
  send: boolean;
  rules: { match?: string; sender?: string; send?: boolean; delay?: number }[];
  room: string;
  sender: string;
  want: boolean;
}

// The same table internal/domain's conformance test holds to ResolveRead, over every
// home the room might have.
describe("whether a receipt is announced, as Go resolves it", () => {
  const cases = table.cases as Case[];
  it("has cases", () => {
    expect(cases.length).toBeGreaterThan(0);
  });
  for (const c of cases) {
    it(c.name, () => {
      const settings = create(DeviceSettingsSchema, {
        sendReceipts: c.send,
        unreadLine: true,
        readRules: c.rules.map((r) => ({ match: r.match ?? "", sender: r.sender ?? "", send: r.send })),
      });
      expect(announces(settings, c.room, c.sender)).toBe(c.want);
    });
  }

  it("is private when the settings could not be read", () => {
    expect(announces(undefined, "matrix:a/!r:x", "@b")).toBe(false);
  });
});

describe("how long the cursor rests on a room before it is read", () => {
  const room = "matrix:a/!r:x";
  const at = (readDelay: number, rules: { match?: string; sender?: string; delay?: number }[], sender = "@b") =>
    readAfter(
      create(DeviceSettingsSchema, {
        readDelay: BigInt(readDelay),
        readRules: rules.map((r) => ({
          match: r.match ?? "",
          sender: r.sender ?? "",
          delay: r.delay === undefined ? undefined : BigInt(r.delay),
        })),
      }),
      room,
      sender,
    );
  it.each([
    ["never, by default", -1, [], -1],
    ["read_delay where no rule names it", 3, [{ match: "matrix:a/!other:x", delay: 0 }], 3],
    ["the room's rule", -1, [{ match: room, delay: 2 }], 2],
    [
      "the narrowest rule, a sender in the room over the room",
      5,
      [
        { match: room, delay: 1 },
        { match: room, sender: "@b", delay: 4 },
      ],
      4,
    ],
    [
      "the narrowest rule's inherited delay, not a wider rule's",
      6,
      [
        { match: room, delay: 1 },
        { match: room, sender: "@b" },
      ],
      6,
    ],
    ["another sender's rule not at all", 2, [{ match: room, sender: "@c", delay: 9 }], 2],
    ["the longer wait where a space's rule might name the room", 1, [{ match: "space:Work", delay: 8 }], 8],
    ["never where a space's rule that might name it says never", 1, [{ match: "space:Work", delay: -1 }], -1],
    [
      "a room's rule over a space's, which cannot beat it",
      1,
      [
        { match: room, delay: 2 },
        { match: "space:Work", delay: -1 },
      ],
      2,
    ],
  ])("%s", (_, readDelay, rules, want) => {
    expect(at(readDelay, rules)).toBe(want);
  });

  it("is never when the settings could not be read", () => {
    expect(readAfter(undefined, room, "@b")).toBe(-1);
  });
});
