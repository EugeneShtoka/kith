import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import { DeviceSchema, PairingOutcome, type StartPairingResponse } from "../gen/backend/v1/services_pb";
import type { JoinBridgeResponse } from "../gen/device/v1/device_pb";
import { Devices, type DevicesCalls, type DevicesView, notListening, said } from "./devices";
import type { StreamEvent } from "./kith";

/** A pairing's token, made up as the test runs. */
const token = ["tok", "7", "x"].join("-");

/** World is kithd and main as the section reaches them, each answer given when the test says. */
class World implements DevicesCalls {
  readonly confirmed: string[] = [];
  readonly removed: string[] = [];
  listed = [create(DeviceSchema, { id: "D1", name: "phone" })];
  pairingListener: ((event: StreamEvent<StartPairingResponse>) => void) | undefined;
  pairingsEnded = 0;
  joinListener: ((event: StreamEvent<JoinBridgeResponse>) => void) | undefined;
  joinsCut = 0;
  removeFails = "";

  devices() {
    return Promise.resolve({ self: "SELF", devices: this.listed });
  }
  remove(id: string) {
    if (this.removeFails !== "") {
      return Promise.reject(new Error(this.removeFails));
    }
    this.removed.push(id);
    this.listed = this.listed.filter((d) => d.id !== id);
    return Promise.resolve();
  }
  startPairing(_name: string, listener: (event: StreamEvent<StartPairingResponse>) => void) {
    this.pairingListener = listener;
    return () => {
      this.pairingsEnded++;
    };
  }
  confirm(t: string, accept: boolean) {
    this.confirmed.push(`${t} ${String(accept)}`);
    return Promise.resolve();
  }
  uri() {
    return Promise.resolve("kith://pair?addr=192.0.2.1%3A7443");
  }
  qr() {
    return Promise.resolve(new Uint8Array([1, 0, 0, 1]));
  }
  join(_uri: string, _name: string, listener: (event: StreamEvent<JoinBridgeResponse>) => void) {
    this.joinListener = listener;
    return () => {
      this.joinsCut++;
    };
  }

  pairingFrame(event: StartPairingResponse["event"]): void {
    this.pairingListener?.({ kind: "frames", frames: [{ event } as StartPairingResponse] });
  }
}

async function settle(): Promise<void> {
  for (let i = 0; i < 5; i++) {
    await Promise.resolve();
  }
}

describe("the devices section", () => {
  it("shows the pairing's code, then the device to confirm, and how it ended", async () => {
    const world = new World();
    let view: DevicesView | undefined;
    const d = new Devices(world, (v) => (view = v));
    d.pair("");
    world.pairingFrame({ case: "ticket", value: { token, addresses: [], id: "SELF" } as never });
    await settle();
    expect(view?.pairing?.uri).toBe("kith://pair?addr=192.0.2.1%3A7443");
    expect(view?.pairing?.qr?.side).toBe(2);
    d.confirm(); // nothing has answered yet
    expect(world.confirmed).toEqual([]);
    world.pairingFrame({
      case: "pending",
      value: { name: "laptop", id: "L1", digits: "123456", role: "device" } as never,
    });
    d.confirm();
    d.confirm(); // once, while it is asked
    await settle();
    expect(world.confirmed).toEqual([`${token} true`]);
    world.pairingFrame({ case: "outcome", value: PairingOutcome.CONFIRMED });
    await settle();
    expect(view?.pairing?.ended).toBe("paired");
    expect(view?.list?.map((x) => x.id)).toEqual(["D1"]);
    d.closePairing();
    expect(world.pairingsEnded).toBe(1);
    world.pairingFrame({ case: "outcome", value: PairingOutcome.REFUSED }); // after it closed: dropped
    expect(view?.pairing).toBeUndefined();
  });

  it("says plainly what to set when kithd listens nowhere", () => {
    const world = new World();
    let view: DevicesView | undefined;
    const d = new Devices(world, (v) => (view = v));
    d.pair("");
    world.pairingListener?.({
      kind: "ended",
      error:
        "[failed_precondition] kithd listens nowhere a device can dial: set [remote] listen in the bridge's config.toml",
    });
    expect(view?.pairing?.ended).toBe("failed");
    expect(notListening(view?.pairing?.note ?? "")).toBe(true);
    expect(view?.pairing?.note.startsWith("kithd listens nowhere")).toBe(true);
  });

  it("unpairs through main and reads the list again either way", async () => {
    const world = new World();
    let view: DevicesView | undefined;
    const d = new Devices(world, (v) => (view = v));
    d.read();
    await settle();
    world.removeFails = "Error invoking remote method 'kith:call': Error: kith: phone stays paired";
    d.unpair(create(DeviceSchema, { id: "D1", name: "phone" }));
    await settle();
    expect(view?.note).toBe("phone was not unpaired: phone stays paired");
    expect(view?.list?.map((x) => x.id)).toEqual(["D1"]);
    world.removeFails = "";
    d.unpair(create(DeviceSchema, { id: "D1", name: "phone" }));
    await settle();
    expect(world.removed).toEqual(["D1"]);
    expect(view?.note).toBe("Unpaired phone.");
    expect(view?.list).toEqual([]);
  });

  it("joins as main allows: the digits, then the bridge; a join dismissed hears nothing more", () => {
    const world = new World();
    let view: DevicesView | undefined;
    const d = new Devices(world, (v) => (view = v));
    d.join(" kith://pair?addr=x ", "");
    expect(view?.join?.stage).toBe("asking");
    world.joinListener?.({ kind: "refused", error: "kith: not joined" });
    expect(view?.join).toEqual({ stage: "failed", digits: "", bridge: "", note: "not joined" });
    d.join("kith://pair?addr=x", "desk");
    world.joinListener?.({
      kind: "frames",
      frames: [{ event: { case: "digits", value: "654321" } } as JoinBridgeResponse],
    });
    expect(view?.join?.digits).toBe("654321");
    world.joinListener?.({
      kind: "frames",
      frames: [{ event: { case: "joined", value: { name: "home" } } } as JoinBridgeResponse],
    });
    world.joinListener?.({ kind: "ended", error: "" });
    expect(view?.join?.stage).toBe("joined");
    expect(view?.join?.bridge).toBe("home");
    d.join("kith://pair?addr=y", "");
    const listener = world.joinListener;
    d.dismissJoin();
    expect(world.joinsCut).toBe(1);
    listener?.({ kind: "frames", frames: [{ event: { case: "digits", value: "1" } } as JoinBridgeResponse] });
    expect(view?.join).toBeUndefined();
  });

  it("says a failure without the wrapping it crossed in", () => {
    expect(said(new Error("Error invoking remote method 'kith:call': Error: kith: the window unpairs only"))).toBe(
      "the window unpairs only",
    );
    expect(said("[not_found] daemon: pair: no pairing waits")).toBe("daemon: pair: no pairing waits");
  });
});
