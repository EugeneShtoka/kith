import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import fc from "fast-check";
import { describe, expect, it } from "vitest";

import { VerificationsResponseSchema } from "../gen/backend/v1/services_pb";
import { VerificationKind } from "../gen/backend/v1/types_pb";
import type { StreamEvent } from "../shared/ipc";
import { Verifying, VerifyRefused } from "./verifications";

const me = "matrix:@me:x";

/** A frame of the Verifications stream, as kithd sends it. */
function frame(kind: VerificationKind, txn: string, from = ""): Uint8Array {
  return toBinary(
    VerificationsResponseSchema,
    create(VerificationsResponseSchema, { verification: { kind, txnId: txn, from } }),
  );
}

/** What happens: kithd's frames and its going away, the window's own ask, answers and moves. */
type Event =
  | { k: "frame"; kind: VerificationKind; txn: string; fromMe: boolean }
  | { k: "asked"; txn: string }
  | { k: "detached" }
  | { k: "profile"; to: string }
  | { k: "answer"; txn: string }
  | { k: "selvesFail"; fail: boolean };

const txns = ["t1", "t2", "t3"];
const event: fc.Arbitrary<Event> = fc.oneof(
  {
    weight: 4,
    arbitrary: fc.record({
      k: fc.constant("frame" as const),
      kind: fc.constantFrom(
        VerificationKind.REQUESTED,
        VerificationKind.SAS,
        VerificationKind.ACCEPTED,
        VerificationKind.DONE,
        VerificationKind.CANCELED,
      ),
      txn: fc.constantFrom(...txns),
      fromMe: fc.boolean(),
    }),
  },
  fc.record({ k: fc.constant("asked" as const), txn: fc.constantFrom(...txns) }),
  fc.constant({ k: "detached" as const }),
  fc.record({ k: fc.constant("profile" as const), to: fc.constantFrom("Home", "Work") }),
  { weight: 3, arbitrary: fc.record({ k: fc.constant("answer" as const), txn: fc.constantFrom(...txns, "") }) },
  fc.record({ k: fc.constant("selvesFail" as const), fail: fc.boolean() }),
);

const tick = () => new Promise((resolve) => setTimeout(resolve, 0));

describe("the verifications the window may answer", () => {
  it("are its own sessions' it was shown or asked for, in its profile, until they end or kithd goes away", async () => {
    await fc.assert(
      fc.asyncProperty(fc.array(event, { maxLength: 40 }), async (events) => {
        let selvesFail = false;
        const asked: string[] = [];
        const v = new Verifying((profile) => {
          asked.push(profile);
          return selvesFail ? Promise.reject(new Error("kithd is not reachable")) : Promise.resolve([me]);
        });
        let profile = "Home";
        const posted: string[] = [];
        const want: string[] = [];
        const post = (e: StreamEvent) => {
          if (e.kind === "frames") {
            for (const f of e.frames) {
              const s = fromBinary(VerificationsResponseSchema, f).verification;
              posted.push(`${String(s?.kind)} ${s?.txnId ?? ""}`);
            }
          }
        };
        v.runIn(profile);
        let stream = v.stream(profile, post);
        /** The model: each transaction the window may answer, and the profile it was held in. */
        const held = new Map<string, string>();
        for (const e of events) {
          switch (e.k) {
            case "frame": {
              stream({ kind: "frames", frames: [frame(e.kind, e.txn, e.fromMe ? me : "matrix:@eve:x")] });
              await tick();
              const theirs = e.kind === VerificationKind.REQUESTED && (!e.fromMe || selvesFail);
              if (!theirs) {
                want.push(`${String(e.kind)} ${e.txn}`);
              }
              if (e.kind === VerificationKind.REQUESTED && !theirs) {
                held.set(e.txn, profile);
              } else if (e.kind === VerificationKind.DONE || e.kind === VerificationKind.CANCELED) {
                held.delete(e.txn);
              }
              break;
            }
            case "asked":
              v.asked(e.txn, profile);
              held.set(e.txn, profile);
              break;
            case "detached":
              stream({ kind: "detached", error: "kithd went away" });
              await tick();
              for (const [txn, p] of held) {
                if (p === profile) {
                  held.delete(txn);
                }
              }
              break;
            case "profile":
              profile = e.to;
              v.runIn(profile);
              for (const [txn, p] of held) {
                if (p !== profile) {
                  held.delete(txn);
                }
              }
              stream = v.stream(profile, post);
              break;
            case "answer": {
              const may = e.txn !== "" && held.get(e.txn) === profile;
              if (may) {
                expect(() => {
                  v.admit(e.txn);
                }).not.toThrow();
              } else {
                expect(() => {
                  v.admit(e.txn);
                }).toThrow(VerifyRefused);
              }
              break;
            }
            case "selvesFail":
              selvesFail = e.fail;
              break;
          }
        }
        // Every frame but someone else's request reaches the window, in order.
        expect(posted).toEqual(want);
        // Who you are is asked in the profile the stream was opened in.
        expect(asked.every((p) => p === "Home" || p === "Work")).toBe(true);
      }),
      { numRuns: 500 },
    );
  });

  it("holds nothing of a profile the window left while it was being shown or asked for", async () => {
    let answer: (ids: string[]) => void = () => undefined;
    const v = new Verifying(() => new Promise((resolve) => (answer = resolve)));
    v.runIn("Home");
    const home = v.stream("Home", () => undefined);
    home({ kind: "frames", frames: [frame(VerificationKind.REQUESTED, "t1", me)] });
    await tick();
    // Who you are is still being asked when the window moves to Work, and StartVerification
    // asked in Home answers there too.
    v.runIn("Work");
    answer([me]);
    v.asked("ours", "Home");
    await tick();
    expect(() => {
      v.admit("t1");
    }).toThrow(VerifyRefused);
    expect(() => {
      v.admit("ours");
    }).toThrow(VerifyRefused);
    // Back in Home, neither is held: the window there now was shown neither.
    v.runIn("Home");
    expect(() => {
      v.admit("t1");
    }).toThrow(VerifyRefused);
  });

  it("posts a stream's events in the order they came, a request held up asking who you are", async () => {
    let answer: (ids: string[]) => void = () => undefined;
    const v = new Verifying(() => new Promise((resolve) => (answer = resolve)));
    v.runIn("Home");
    const got: string[] = [];
    const post = v.stream("Home", (e) => got.push(e.kind === "frames" ? `frames ${String(e.frames.length)}` : e.kind));
    post({ kind: "attached" });
    post({ kind: "frames", frames: [frame(VerificationKind.REQUESTED, "t1", me)] });
    post({ kind: "frames", frames: [frame(VerificationKind.SAS, "t1")] });
    post({ kind: "detached", error: "gone" });
    await tick();
    expect(got).toEqual(["attached"]);
    answer([me]);
    await tick();
    expect(got).toEqual(["attached", "frames 1", "frames 1", "detached"]);
  });
});
