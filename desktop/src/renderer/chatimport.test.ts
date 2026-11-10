import { create, toBinary } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import {
  type ChatImportSendersResponse,
  ChatImportSendersResponseSchema,
  PreviewChatImportResponseSchema,
} from "../gen/backend/v1/services_pb";
import type { ChatChosen } from "../shared/ipc";
import { ChatImport, type ImportCalls, type ImportView } from "./chatimport";

const dana = { userId: "whatsapp:4915550001", displayName: "Dana W." };
const lee = { userId: "whatsapp:4915550002", displayName: "Lee Park" };

const chosen: ChatChosen = {
  upload: "u1",
  zone: "Europe/Berlin",
  preview: toBinary(
    PreviewChatImportResponseSchema,
    create(PreviewChatImportResponseSchema, {
      title: "WhatsApp Chat - Dana",
      messages: 40,
      rooms: [{ roomId: "whatsapp:9/1", found: 3, adds: 30 }],
    }),
  ),
};

const senders: ChatImportSendersResponse = create(ChatImportSendersResponseSchema, {
  adds: 30,
  members: [dana, lee],
  senders: [
    { name: "Dana", messages: 25, chosen: dana.userId, likely: [dana] },
    { name: "Mum", messages: 15 },
  ],
});

/** A call kithd or main has not answered yet, answered or failed when the test says. */
interface Held<T> {
  answer: (value: T) => void;
  fail: (err: Error) => void;
}

/** Calls whose answers the test gives, in whatever order it picks; applied keeps each import asked. */
class Calls implements ImportCalls {
  readonly chosen: Held<ChatChosen | undefined>[] = [];
  readonly matched: Held<ChatImportSendersResponse>[] = [];
  readonly applied: { room: string; senders: Readonly<Record<string, string>>; held: Held<number> }[] = [];

  choose(): Promise<ChatChosen | undefined> {
    return new Promise((answer, fail) => this.chosen.push({ answer, fail }));
  }

  senders(upload: string, zone: string): Promise<ChatImportSendersResponse> {
    expect([upload, zone]).toEqual(["u1", "Europe/Berlin"]);
    return new Promise((answer, fail) => this.matched.push({ answer, fail }));
  }

  apply(upload: string, _zone: string, room: string, who: Readonly<Record<string, string>>): Promise<number> {
    expect(upload).toBe("u1");
    return new Promise((answer, fail) => this.applied.push({ room, senders: who, held: { answer, fail } }));
  }
}

function leading(): { calls: Calls; imp: ChatImport; view: () => ImportView } {
  const calls = new Calls();
  let view: ImportView | undefined;
  const imp = new ChatImport(calls, (v) => (view = v));
  return { calls, imp, view: () => view ?? imp.snapshot() };
}

/** settled lets the answers given reach the import. */
const settled = () => new Promise((resolve) => setTimeout(resolve, 0));

describe("a chat import", () => {
  it("goes into the chat chosen, each name as kithd was sure of unless chosen otherwise", async () => {
    const { calls, imp, view } = leading();
    imp.choose();
    expect(view()).toMatchObject({ step: "busy", note: "Reading the export…" });
    calls.chosen[0]?.answer(chosen);
    await settled();
    expect(view().step).toBe("chat");
    expect(view().preview?.rooms[0]?.roomId).toBe("whatsapp:9/1");
    imp.room("whatsapp:9/1", "Dana");
    calls.matched[0]?.answer(senders);
    await settled();
    expect(view()).toMatchObject({ step: "senders", who: { Dana: dana.userId } });
    imp.whoIs("Mum", lee.userId);
    imp.apply();
    expect(calls.applied[0]).toMatchObject({ room: "whatsapp:9/1", senders: { Dana: dana.userId, Mum: lee.userId } });
    calls.applied[0]?.held.answer(30);
    await settled();
    expect(view()).toMatchObject({ step: "idle", note: "Imported 30 messages into Dana." });
  });

  it("drops an answer for a step canceled or left meanwhile, and nothing picked changes nothing", async () => {
    const { calls, imp, view } = leading();
    imp.choose();
    imp.cancel();
    calls.chosen[0]?.answer(chosen);
    await settled();
    expect(view()).toMatchObject({ step: "idle", note: "" });
    imp.choose();
    calls.chosen[1]?.answer(undefined);
    await settled();
    expect(view()).toMatchObject({ step: "idle", note: "" });
    imp.room("whatsapp:9/1", "Dana"); // no export: nothing to match
    expect(calls.matched).toHaveLength(0);
    imp.choose();
    calls.chosen[2]?.answer(chosen);
    await settled();
    imp.room("whatsapp:9/1", "Dana");
    imp.cancel();
    calls.matched[0]?.answer(senders);
    await settled();
    expect(view().step).toBe("idle");
  });

  it("goes back a step, said why, when one fails, and can be tried again", async () => {
    const { calls, imp, view } = leading();
    imp.choose();
    calls.chosen[0]?.fail(new Error("the file holds no exported chat"));
    await settled();
    expect(view()).toMatchObject({ step: "idle", note: "Could not read the export: the file holds no exported chat" });
    imp.choose();
    calls.chosen[1]?.answer(chosen);
    await settled();
    imp.room("whatsapp:9/1", "Dana");
    calls.matched[0]?.answer(senders);
    await settled();
    imp.whoIs("Mum", "");
    imp.apply();
    calls.applied[0]?.held.fail(new Error("disk full"));
    await settled();
    expect(view()).toMatchObject({ step: "senders", note: "Could not import the chat: disk full", who: { Mum: "" } });
    imp.apply();
    calls.applied[1]?.held.answer(1);
    await settled();
    expect(view().note).toBe("Imported 1 message into Dana.");
  });
});
