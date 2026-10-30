import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import table from "../../../internal/domain/testdata/read.json";
import { DeviceSettingsSchema } from "../gen/device/v1/device_pb";
import { announces } from "./read";

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
