import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import { IdentitySchema } from "../gen/backend/v1/services_pb";
import { pictureChoices } from "./identity";

describe("a person's picture choices", () => {
  it("are each account's own, one account's for all, this one's among them, and a picture saved for them", () => {
    const dana = create(IdentitySchema, { alias: "Dana", ids: ["telegram:1", "whatsapp:2"], picture: "file:ab" });
    expect(pictureChoices(dana, "slack:T.U3").map((c) => c.value)).toEqual([
      "",
      "telegram:1",
      "whatsapp:2",
      "slack:T.U3",
      "file:ab",
    ]);
    expect(pictureChoices(dana, "whatsapp:2").map((c) => c.value)).toEqual(["", "telegram:1", "whatsapp:2", "file:ab"]);
    expect(pictureChoices(undefined, "telegram:9").map((c) => c.value)).toEqual(["", "telegram:9"]);
  });
});
