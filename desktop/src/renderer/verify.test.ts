import { create, type MessageInitShape } from "@bufbuild/protobuf";
import fc from "fast-check";
import { describe, expect, it } from "vitest";

import { AccountSchema } from "../gen/backend/v1/services_pb";
import { type Verification, VerificationKind, VerificationSchema } from "../gen/backend/v1/types_pb";
import {
  answerable,
  heard,
  answering,
  asked,
  canceled,
  type Flow,
  lost,
  noVerify,
  stepped,
  type Verify,
  verifyAs,
} from "./verify";

const step = (
  kind: VerificationKind,
  txnId: string,
  more: MessageInitShape<typeof VerificationSchema> = {},
): Verification => create(VerificationSchema, { kind, txnId, ...more });
const request = (txn: string) => step(VerificationKind.REQUESTED, txn, { from: "@me:x", device: "PHONE" });
const sas = (txn: string) =>
  step(VerificationKind.SAS, txn, {
    emojis: [
      { glyph: "🐶", name: "Dog" },
      { glyph: "🎸", name: "Guitar" },
    ],
  });

const run = (v: Verify, ...steps: Verification[]) => steps.reduce(stepped, v);

describe("a verification", () => {
  it("asked from elsewhere is accepted, its emoji compared and confirmed, and ends verified", () => {
    let v = run(noVerify, request("t1"));
    expect(v.flow).toMatchObject({ txn: "t1", ours: false, stage: "requested", from: "@me:x", device: "PHONE" });
    expect(answerable(v.flow)).toBe(true);
    v = answering(v);
    expect(answerable(v.flow)).toBe(false);
    v = heard(v, "t1", "requested", "");
    v = run(v, step(VerificationKind.ACCEPTED, "t1"), sas("t1"));
    expect(v.flow?.stage).toBe("sas");
    expect(v.flow?.emojis.map((e) => e.name)).toEqual(["Dog", "Guitar"]);
    expect(answerable(v.flow)).toBe(true);
    v = heard(answering(v), "t1", "sas", "");
    v = run(v, step(VerificationKind.CONFIRMED, "t1"), step(VerificationKind.DONE, "t1"));
    expect(v.flow).toBeUndefined();
    expect(v.said).toEqual(["This session is verified."]);
    v = run(v, step(VerificationKind.RESTORED, "", { reason: "restored 4 room keys from backup" }));
    expect(v.said).toEqual(["This session is verified.", "restored 4 room keys from backup"]);
  });

  it("asked from here can only be called off until the other session accepts it", () => {
    let v = asked(noVerify, "t1");
    expect(v.flow).toMatchObject({ txn: "t1", ours: true, stage: "requested" });
    expect(answerable(v.flow)).toBe(false);
    v = run(v, request("t1")); // our own, echoed
    expect(v.flow?.ours).toBe(true);
    v = run(v, sas("t1"));
    expect(answerable(v.flow)).toBe(true);
    expect(canceled(v)).toEqual({ flow: undefined, said: ["Verification canceled."], asking: false });
  });

  it("answered in another window waits here, and says so", () => {
    let v = run(noVerify, request("t1"), step(VerificationKind.ACCEPTED, "t1"));
    expect(answerable(v.flow)).toBe(false);
    expect(v.flow).toMatchObject({ answered: true, mine: false });
    v = run(v, sas("t1"), step(VerificationKind.CONFIRMED, "t1"));
    expect(v.flow).toMatchObject({ stage: "sas", answered: true, mine: false });
  });

  it("answered here a moment after another window keeps the other's answer standing", () => {
    let v = answering(run(noVerify, request("t1")));
    v = run(v, step(VerificationKind.ACCEPTED, "t1"));
    v = heard(v, "t1", "requested", "daemon: that verification step was answered already");
    expect(v.flow).toMatchObject({ answered: true, mine: false, sending: false, failed: "" });
    expect(answerable(v.flow)).toBe(false);
  });

  it("keeps the emoji to answer when the acceptance arrives after them", () => {
    const v = run(noVerify, request("t1"), sas("t1"), step(VerificationKind.ACCEPTED, "t1"));
    expect(answerable(v.flow)).toBe(true);
    // This window's acceptance coming back after the emoji, too.
    const slow = run(answering(run(noVerify, request("t1"))), sas("t1"));
    expect(heard(slow, "t1", "requested", "")).toBe(slow);
  });

  it("canceled by the other session mid-compare ends, and a confirm coming back after changes nothing", () => {
    let v = answering(run(noVerify, request("t1"), sas("t1")));
    v = run(v, step(VerificationKind.CANCELED, "t1", { reason: "the other device said they do not match" }));
    expect(v.flow).toBeUndefined();
    expect(v.said).toEqual(["Verification canceled: the other device said they do not match"]);
    expect(heard(v, "t1", "sas", "")).toBe(v);
    expect(heard(v, "t1", "sas", "unknown transaction ID")).toBe(v);
  });

  it("timed out ends, and a confirm refused after it changes nothing", () => {
    let v = run(noVerify, request("t1"), sas("t1"));
    v = answering(v);
    v = run(v, step(VerificationKind.CANCELED, "t1", { reason: "verification timed out" }));
    expect(heard(v, "t1", "sas", "failed to get transaction t1: unknown transaction ID")).toBe(v);
    expect(v.said).toEqual(["Verification canceled: verification timed out"]);
  });

  it("ends when the window loses kithd, which forgets it on a restart", () => {
    const v = lost(run(noVerify, request("t1"), sas("t1")));
    expect(v.flow).toBeUndefined();
    expect(v.said[0]).toMatch(/lost mid-verification/);
    expect(lost(noVerify)).toBe(noVerify);
  });

  it("names what failed, and offers the answer again", () => {
    let v = answering(run(noVerify, request("t1")));
    v = heard(v, "t1", "requested", "kithd is not reachable");
    expect(v.flow?.failed).toBe("kithd is not reachable");
    expect(answerable(v.flow)).toBe(true);
  });

  it("is asked for from the only Matrix account, else the default, else any", () => {
    const a = (on: string, network = "Matrix", isDefault = false) =>
      create(AccountSchema, { on, network, name: on, default: isDefault });
    expect(verifyAs([a("m1"), a("t1", "Telegram")]).map((x) => x.on)).toEqual(["m1"]);
    expect(verifyAs([a("m1"), a("m2", "Matrix", true)]).map((x) => x.on)).toEqual(["m2"]);
    expect(verifyAs([a("m1"), a("m2")]).map((x) => x.on)).toEqual(["m1", "m2"]);
    expect(verifyAs([a("t1", "Telegram")])).toEqual([]);
  });
});

/** What happens to the window, in any order: kithd's steps, and the window's own moves. */
type Event =
  | { k: "step"; kind: VerificationKind; txn: string }
  | { k: "asked"; txn: string }
  | { k: "answer" }
  | { k: "back"; i: number; error: string }
  | { k: "cancel" }
  | { k: "lost" };

const txns = ["t1", "t2"];
const event: fc.Arbitrary<Event> = fc.oneof(
  {
    weight: 4,
    arbitrary: fc.record({
      k: fc.constant("step" as const),
      kind: fc.constantFrom(
        VerificationKind.REQUESTED,
        VerificationKind.SAS,
        VerificationKind.ACCEPTED,
        VerificationKind.CONFIRMED,
        VerificationKind.DONE,
        VerificationKind.CANCELED,
        VerificationKind.RESTORED,
      ),
      txn: fc.constantFrom(...txns),
    }),
  },
  fc.record({ k: fc.constant("asked" as const), txn: fc.constantFrom(...txns) }),
  { weight: 2, arbitrary: fc.constant({ k: "answer" as const }) },
  {
    weight: 2,
    arbitrary: fc.record({
      k: fc.constant("back" as const),
      i: fc.nat(),
      error: fc.constantFrom("", "refused", "unreachable"),
    }),
  },
  fc.constant({ k: "cancel" as const }),
  fc.constant({ k: "lost" as const }),
);

describe("whatever arrives in whatever order", () => {
  it("shows a verification only until it ends, and never offers an answer kithd says was given", () => {
    fc.assert(
      fc.property(fc.array(event, { maxLength: 40 }), (events) => {
        let v = noVerify;
        /** Answers sent and not back: the transaction and stage each answered. */
        const out: { txn: string; stage: Flow["stage"] }[] = [];
        /** Transactions that ended here, until asked or requested again. */
        const ended = new Set<string>();
        /** kithd said the stage shown of the flow shown was answered. */
        let given = false;
        for (const e of events) {
          const before = v;
          const shown = v.flow;
          switch (e.k) {
            case "step": {
              v = stepped(v, e.kind === VerificationKind.SAS ? sas(e.txn) : step(e.kind, e.txn));
              const mine = shown?.txn === e.txn;
              if (e.kind === VerificationKind.REQUESTED && !(mine && shown.ours)) {
                ended.delete(e.txn);
                given = false;
              } else if (!mine && e.kind !== VerificationKind.RESTORED) {
                expect(v).toBe(before); // another verification's step
              } else if (mine && (e.kind === VerificationKind.DONE || e.kind === VerificationKind.CANCELED)) {
                ended.add(e.txn);
              } else if (mine && e.kind === VerificationKind.SAS) {
                given = false;
              } else if (
                mine &&
                ((e.kind === VerificationKind.ACCEPTED && shown.stage === "requested") ||
                  (e.kind === VerificationKind.CONFIRMED && shown.stage === "sas"))
              ) {
                given = true;
              }
              break;
            }
            case "asked":
              v = asked(v, e.txn);
              ended.delete(e.txn);
              given = false;
              break;
            case "answer":
              if (answerable(v.flow)) {
                out.push({ txn: v.flow.txn, stage: v.flow.stage });
                v = answering(v);
              }
              break;
            case "back": {
              const a = out.splice(e.i % Math.max(out.length, 1), 1)[0];
              if (a === undefined) {
                break;
              }
              v = heard(v, a.txn, a.stage, e.error);
              if (shown?.txn !== a.txn || shown.stage !== a.stage) {
                expect(v).toBe(before); // an answer to what is no longer shown
              } else if (e.error === "") {
                given = true;
              }
              break;
            }
            case "cancel":
              if (shown !== undefined) {
                ended.add(shown.txn);
              }
              v = canceled(v);
              break;
            case "lost":
              if (shown !== undefined) {
                ended.add(shown.txn);
              }
              v = lost(v);
              break;
          }
          if (v.flow !== undefined) {
            expect(ended.has(v.flow.txn)).toBe(false);
            if (given) {
              expect(answerable(v.flow)).toBe(false);
            }
            if (v.flow.ours && v.flow.stage === "requested") {
              expect(answerable(v.flow)).toBe(false);
            }
          }
        }
      }),
      { numRuns: 2000 },
    );
  });
});
