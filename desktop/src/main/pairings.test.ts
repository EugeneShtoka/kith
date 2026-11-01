import { create } from "@bufbuild/protobuf";
import fc from "fast-check";
import { describe, expect, it } from "vitest";

import {
  type Device,
  DeviceSchema,
  type PendingDevice,
  PairingOutcome,
  PendingDeviceSchema,
  StartPairingResponseSchema,
} from "../gen/backend/v1/services_pb";
import { InviteResponseSchema, JoinBridgeResponseSchema } from "../gen/device/v1/device_pb";
import { type Join, PairingRefused, Pairings, type Run } from "./pairings";

/** A question main put to the person, answered when the test says. */
interface Asked<T> {
  what: T;
  own?: boolean | undefined;
  answer: (yes: boolean) => void;
  answered: boolean;
  /** What the person said. */
  said?: boolean;
}

/** tokenOf is a pairing's token, made up as the test runs (never written out whole). */
const tokenOf = (n: number) => ["tok", String(n), "x"].join("-");

/** The devices that answer a pairing: two, so a second answer can differ from the first. */
const answering: [PendingDevice, PendingDevice] = [
  create(PendingDeviceSchema, { name: "laptop", id: "AAAA", digits: "111111", role: "device" }),
  create(PendingDeviceSchema, { name: "phone", id: "BBBB", digits: "222222", role: "device" }),
];

/** The devices kithd lists; "OWN" is the window's own. */
const paired: Device[] = ["OWN", "D1", "D2", "D3"].map((id) => create(DeviceSchema, { id, name: id.toLowerCase() }));

type Event =
  | { kind: "open" }
  | { kind: "ticket"; run: number }
  | { kind: "pending"; run: number; device: number }
  | { kind: "outcome"; run: number }
  | { kind: "streamEnds"; run: number }
  | { kind: "close"; run: number }
  | { kind: "confirm"; run: number; forged: boolean; accept: boolean }
  | { kind: "answerPair"; pick: number; yes: boolean }
  | { kind: "profile"; to: number }
  | { kind: "restart" }
  | { kind: "devices"; ids: number[]; stale: boolean }
  | { kind: "remove"; id: number; ownFails: boolean; sendFails: boolean }
  | { kind: "answerUnpair"; pick: number; yes: boolean }
  | { kind: "join"; bad: boolean }
  | { kind: "answerJoin"; pick: number; yes: boolean }
  | { kind: "joinFrame"; joined: boolean }
  | { kind: "joinEnds" }
  | { kind: "joinCut" };

/** run picks a pairing the window opened, counting back from the last. */
const run = fc.oneof({ arbitrary: fc.constant(0), weight: 4 }, { arbitrary: fc.nat(2), weight: 1 });
const answer = fc.record({
  pick: fc.nat(),
  yes: fc.oneof({ arbitrary: fc.constant(true), weight: 3 }, { arbitrary: fc.boolean(), weight: 1 }),
});

const event: fc.Arbitrary<Event> = fc.oneof(
  { arbitrary: fc.constant({ kind: "open" as const }), weight: 2 },
  { arbitrary: fc.record({ kind: fc.constant("ticket" as const), run }), weight: 3 },
  { arbitrary: fc.record({ kind: fc.constant("pending" as const), run, device: fc.nat(1) }), weight: 3 },
  { arbitrary: fc.record({ kind: fc.constant("outcome" as const), run }), weight: 1 },
  { arbitrary: fc.record({ kind: fc.constant("streamEnds" as const), run }), weight: 1 },
  { arbitrary: fc.record({ kind: fc.constant("close" as const), run }), weight: 1 },
  {
    arbitrary: fc.record({ kind: fc.constant("confirm" as const), run, forged: fc.boolean(), accept: fc.boolean() }),
    weight: 1,
  },
  {
    arbitrary: fc.record({
      kind: fc.constant("confirm" as const),
      run,
      forged: fc.constant(false),
      accept: fc.constant(true),
    }),
    weight: 4,
  },
  { arbitrary: answer.map((a) => ({ kind: "answerPair" as const, ...a })), weight: 4 },
  { arbitrary: fc.record({ kind: fc.constant("profile" as const), to: fc.nat(1) }), weight: 1 },
  { arbitrary: fc.constant({ kind: "restart" as const }), weight: 1 },
  {
    arbitrary: fc.record({
      kind: fc.constant("devices" as const),
      ids: fc.subarray([0, 1, 2, 3]),
      stale: fc.boolean(),
    }),
    weight: 2,
  },
  {
    arbitrary: fc.record({
      kind: fc.constant("remove" as const),
      id: fc.nat(3),
      ownFails: fc.boolean(),
      sendFails: fc.boolean(),
    }),
    weight: 3,
  },
  { arbitrary: answer.map((a) => ({ kind: "answerUnpair" as const, ...a })), weight: 3 },
  { arbitrary: fc.record({ kind: fc.constant("join" as const), bad: fc.boolean() }), weight: 2 },
  { arbitrary: answer.map((a) => ({ kind: "answerJoin" as const, ...a })), weight: 2 },
  { arbitrary: fc.record({ kind: fc.constant("joinFrame" as const), joined: fc.boolean() }), weight: 2 },
  { arbitrary: fc.constant({ kind: "joinEnds" as const }), weight: 1 },
  { arbitrary: fc.constant({ kind: "joinCut" as const }), weight: 1 },
);

/** settle lets every answer due arrive. */
async function settle(): Promise<void> {
  for (let i = 0; i < 5; i++) {
    await Promise.resolve();
  }
}

/** What the test knows of a pairing the window opened, as kithd and the window had it. */
interface Opened {
  run: Run;
  token: string;
  profile: string;
  load: number;
  ticket: boolean;
  /** The first device that answered; spoiled: another answered after it. */
  pending: PendingDevice | undefined;
  spoiled: boolean;
  /** The pairing is over: kithd said how it ended, its stream ended, or it was cut. */
  over: boolean;
  /** Its stream was cut from main's side (kithd abandons the pairing). */
  cut: boolean;
  accepted: number;
}

describe("the pairings the window opens, the devices it unpairs, the bridge it joins", () => {
  // Whatever the window asks and whenever the person and kithd answer: a pairing is
  // confirmed only for a pairing the window opened in this load and profile, still open,
  // with the one device that answered it, once, after the person said yes to that very
  // device; a refusal goes only to such a pairing too; a device is unpaired only if the
  // last list in this load and profile showed it, after the person said yes having been
  // told whether it is the window's own; a join is opened only after the person's yes in
  // the same load, one at a time; and the relay is started again exactly when the device
  // file may name the bridge since.
  it("change nothing the person did not say yes to, there and then", async () => {
    await fc.assert(
      fc.asyncProperty(fc.array(event, { maxLength: 80, size: "max" }), async (events) => {
        const pairAsks: Asked<PendingDevice>[] = [];
        const unpairAsks: Asked<Device>[] = [];
        const joinAsks: Asked<string>[] = [];
        const pairings = new Pairings({
          pair: (device) =>
            new Promise((resolve) => {
              pairAsks.push({ what: device, answer: resolve, answered: false });
            }),
          unpair: (device, own) =>
            new Promise((resolve) => {
              unpairAsks.push({ what: device, own, answer: resolve, answered: false });
            }),
          join: (invite) =>
            new Promise((resolve) => {
              joinAsks.push({ what: invite.id, answer: resolve, answered: false });
            }),
        });
        let load = 0;
        let profile = "Home";
        pairings.runIn(profile);
        const opened: Opened[] = [];
        let tokens = 0;
        /** The last Devices answer the window was given here, as the test knows it. */
        let listed: { load: number; profile: string; ids: Set<string> } | undefined;
        /** The join opened, as the gate would open it, and whether it may have been written. */
        let joining: { join: Join; load: number; digits: boolean; joined: boolean } | undefined;
        let rejoins = 0;
        let rejoinsDue = 0;
        let removing = false;
        /** What a call failed with besides a refusal or the failure the test gave it: a broken expectation. */
        const problems: unknown[] = [];
        const failed = (err: unknown) => {
          const given = err instanceof Error && /^(no such device|not a kith)/.test(err.message);
          if (!(err instanceof PairingRefused) && !given) {
            problems.push(err);
          }
        };

        const live = (o: Opened) => !o.over && o.load === load && o.profile === profile;
        const cut = (o: Opened) => {
          if (!o.over) {
            o.over = true;
            o.cut = true;
          }
        };
        /** endJoin is the join's stream ending (kithd's end) or cut (the window's), as the gate has it. */
        const endJoin = (wasCut: boolean) => {
          const j = joining;
          if (j === undefined) {
            return;
          }
          joining = undefined;
          if (j.joined || (wasCut && j.digits)) {
            rejoinsDue++;
          }
          if (wasCut) {
            j.join.stop();
          } else if (pairings.joinEnded(j.join, false)) {
            rejoins++;
          }
        };

        for (const e of events) {
          switch (e.kind) {
            case "open": {
              const busy = opened.some((x) => !x.over);
              let o: Opened | undefined = undefined;
              try {
                const run = pairings.opened(() => {
                  if (o !== undefined) {
                    cut(o);
                  }
                });
                o = {
                  run,
                  token: tokenOf(++tokens),
                  profile,
                  load,
                  ticket: false,
                  pending: undefined,
                  spoiled: false,
                  over: false,
                  cut: false,
                  accepted: 0,
                };
                opened.push(o);
              } catch (err) {
                expect(err).toBeInstanceOf(PairingRefused);
              }
              expect(o === undefined, "a second pairing open at once, or one refused with none open").toBe(busy);
              break;
            }
            case "ticket": {
              const o = opened[opened.length - 1 - e.run];
              if (o && !o.over && !o.ticket) {
                o.ticket = true;
                pairings.frame(
                  o.run,
                  create(StartPairingResponseSchema, { event: { case: "ticket", value: { token: o.token } } }),
                );
              }
              break;
            }
            case "pending": {
              const o = opened[opened.length - 1 - e.run];
              if (o && !o.over && o.ticket) {
                const d = e.device === 0 ? answering[0] : answering[1];
                if (o.pending === undefined) {
                  o.pending = d;
                } else if (o.pending.id !== d.id) {
                  o.spoiled = true;
                }
                pairings.frame(o.run, create(StartPairingResponseSchema, { event: { case: "pending", value: d } }));
              }
              break;
            }
            case "outcome": {
              const o = opened[opened.length - 1 - e.run];
              if (o && !o.over) {
                o.over = true;
                pairings.frame(
                  o.run,
                  create(StartPairingResponseSchema, { event: { case: "outcome", value: PairingOutcome.CONFIRMED } }),
                );
              }
              break;
            }
            case "streamEnds":
            case "close": {
              const o = opened[opened.length - 1 - e.run];
              if (o) {
                if (!o.over) {
                  o.over = true;
                  o.cut = e.kind === "close";
                }
                pairings.end(o.run);
              }
              break;
            }
            case "confirm": {
              const o = opened[opened.length - 1 - e.run];
              const token = e.forged || o === undefined ? tokenOf(1000 + e.run) : o.token;
              const wasLive = o !== undefined && !e.forged && o.ticket && live(o);
              const askable = wasLive && o.pending !== undefined && !o.spoiled;
              const asksBefore = pairAsks.length;
              const call = pairings.confirm(token, e.accept, (accept) => {
                expect(o, "a confirmation sent for a pairing the window did not open").toBeDefined();
                if (o === undefined) {
                  return Promise.resolve();
                }
                expect(e.forged, "a confirmation sent for a token the window was never given").toBe(false);
                expect(live(o), "an answer sent for a pairing ended, of another load or another profile").toBe(true);
                if (accept) {
                  const ask = pairAsks[asksBefore];
                  expect(ask?.said, "paired with no yes in main's dialog").toBe(true);
                  expect(ask?.what, "paired a device other than the one shown").toBe(o.pending);
                  expect(o.spoiled, "paired although two devices answered").toBe(false);
                  expect(o.accepted, "a pairing confirmed twice").toBe(0);
                  o.accepted++;
                }
                o.over = true; // answered: kithd ends the pairing
                return Promise.resolve();
              });
              call.catch(failed);
              await settle();
              if (!askable) {
                expect(pairAsks.length, "the person asked about a pairing with nothing of the window's to pair").toBe(
                  asksBefore,
                );
              }
              break;
            }
            case "answerPair":
            case "answerUnpair":
            case "answerJoin": {
              const asks: Asked<unknown>[] =
                e.kind === "answerPair" ? pairAsks : e.kind === "answerUnpair" ? unpairAsks : joinAsks;
              const open = asks.filter((a) => !a.answered);
              const a = open[e.pick % Math.max(open.length, 1)];
              if (a) {
                a.answered = true;
                a.said = e.yes;
                a.answer(e.yes);
                await settle();
              }
              break;
            }
            case "profile": {
              const to = ["Home", "Work"][e.to] ?? "Home";
              if (to !== profile) {
                profile = to;
                listed = undefined;
                for (const o of opened) {
                  if (!o.over && o.profile !== to) {
                    cut(o);
                  }
                }
              }
              pairings.runIn(to);
              break;
            }
            case "restart": {
              load++;
              listed = undefined;
              for (const o of opened) {
                cut(o);
              }
              pairings.runIn("");
              {
                const j = joining; // the gate's stop of it runs inside restart
                joining = undefined;
                if (j !== undefined && (j.joined || j.digits)) {
                  rejoinsDue++;
                }
              }
              pairings.restart();
              profile = "Home";
              pairings.runIn(profile);
              await settle();
              break;
            }
            case "devices": {
              const ids = e.ids.map((i) => paired[i]?.id ?? "");
              const devices = paired.filter((d) => ids.includes(d.id));
              const at = e.stale ? (profile === "Home" ? "Work" : "Home") : profile;
              pairings.devices(devices, at);
              if (!e.stale) {
                listed = { load, profile, ids: new Set(ids) };
              }
              break;
            }
            case "remove": {
              const id = paired[e.id]?.id ?? "";
              const shown =
                listed !== undefined && listed.load === load && listed.profile === profile && listed.ids.has(id);
              const wasRemoving = removing;
              const asksBefore = unpairAsks.length;
              const at = listed;
              if (shown && !wasRemoving) {
                removing = true;
              }
              const call = pairings.remove(
                id,
                () => (e.ownFails ? Promise.reject(new Error("the relay is away")) : Promise.resolve("OWN")),
                () => {
                  const ask = unpairAsks[asksBefore];
                  expect(shown, `unpaired ${id}, which the window's last list did not show`).toBe(true);
                  expect(wasRemoving, "two unpairings at once").toBe(false);
                  expect(ask?.said, "unpaired with no yes in main's dialog").toBe(true);
                  expect(ask?.what.id, "unpaired another device than the one asked about").toBe(id);
                  expect(ask?.own, "the person was not told whether it is the window's own").toBe(
                    e.ownFails ? undefined : id === "OWN",
                  );
                  expect(
                    listed === at && at?.load === load && at.profile === profile,
                    "unpaired from a list since gone",
                  ).toBe(true);
                  return e.sendFails
                    ? Promise.reject(new Error("no such device: unpaired meanwhile"))
                    : Promise.resolve();
                },
              );
              void call.then(
                () => {
                  if (!wasRemoving && shown) {
                    removing = false;
                  }
                  at?.ids.delete(id);
                },
                (err: unknown) => {
                  if (!wasRemoving && shown) {
                    removing = false;
                  }
                  failed(err);
                },
              );
              await settle();
              if (!shown || wasRemoving) {
                expect(unpairAsks.length, "the person asked about a device the window may not unpair").toBe(asksBefore);
              }
              break;
            }
            case "join": {
              const before = joinAsks.length;
              const at = load;
              const call = pairings.join(() =>
                e.bad
                  ? Promise.reject(new Error("not a kith://pair URI"))
                  : Promise.resolve(create(InviteResponseSchema, { id: "BRIDGE", addresses: ["192.0.2.1:7443"] })),
              );
              call.then((join) => {
                const ask = joinAsks[before];
                expect(joining, "a second join opened while one is").toBeUndefined();
                expect(e.bad, "joined a URI that does not read").toBe(false);
                expect(ask?.said, "joined with no yes in main's dialog").toBe(true);
                expect(at, "joined after the window reloaded").toBe(load);
                joining = { join, load: at, digits: false, joined: false };
                join.stop = () => {
                  if (pairings.joinEnded(join, true)) {
                    rejoins++;
                  }
                };
              }, failed);
              await settle();
              break;
            }
            case "joinFrame": {
              const j = joining;
              if (j !== undefined) {
                if (e.joined) {
                  j.joined = true;
                  pairings.joinFrame(
                    j.join,
                    create(JoinBridgeResponseSchema, { event: { case: "joined", value: {} } }),
                  );
                } else {
                  j.digits = true;
                  pairings.joinFrame(
                    j.join,
                    create(JoinBridgeResponseSchema, { event: { case: "digits", value: "1" } }),
                  );
                }
              }
              break;
            }
            case "joinEnds":
              endJoin(false);
              break;
            case "joinCut":
              endJoin(true);
              break;
          }
        }
        expect(problems, "a broken expectation").toEqual([]);
        expect(rejoins, "the relay started again other than when the device file may name the bridge").toBe(rejoinsDue);
      }),
      { numRuns: 1500 },
    );
  });

  // The person's yes, given while nothing changed, is acted on: the device that answered
  // is paired, the device listed unpaired, the bridge joined.
  it("act on a yes given for what is still so", async () => {
    const pairings = new Pairings({
      pair: () => Promise.resolve(true),
      unpair: () => Promise.resolve(true),
      join: () => Promise.resolve(true),
    });
    pairings.runIn("Home");
    const run = pairings.opened(() => undefined);
    pairings.frame(
      run,
      create(StartPairingResponseSchema, { event: { case: "ticket", value: { token: tokenOf(1) } } }),
    );
    pairings.frame(run, create(StartPairingResponseSchema, { event: { case: "pending", value: answering[0] } }));
    const sent: string[] = [];
    await pairings.confirm(tokenOf(1), true, (accept) => {
      sent.push(`confirm ${String(accept)}`);
      return Promise.resolve();
    });
    pairings.devices(paired, "Home");
    await pairings.remove(
      "D1",
      () => Promise.resolve("OWN"),
      () => {
        sent.push("remove D1");
        return Promise.resolve();
      },
    );
    const join = await pairings.join(() => Promise.resolve(create(InviteResponseSchema, { id: "BRIDGE" })));
    pairings.joinFrame(join, create(JoinBridgeResponseSchema, { event: { case: "joined", value: {} } }));
    expect(sent).toEqual(["confirm true", "remove D1"]);
    expect(pairings.joinEnded(join, false)).toBe(true);
  });
});
