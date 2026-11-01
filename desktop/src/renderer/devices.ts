// The devices paired with this kithd, as the settings page shows them: listing them,
// pairing another by QR code (as `kith pair` does), unpairing one, and joining a bridge
// from this device (as `kith join` does). Confirming a pairing, unpairing and joining are
// each asked again by main in a dialog of its own, which alone sends them: the window's
// buttons only ask main to ask (main/pairings.ts).
import { type Device, PairingOutcome, type StartPairingResponse } from "../gen/backend/v1/services_pb";
import type { JoinBridgeResponse } from "../gen/device/v1/device_pb";
import type { StreamEvent } from "./kith";
import { type QR, sideOf } from "./login";

/** What Devices reaches: kithd's calls and streams through main, and the relay's. */
export interface DevicesCalls {
  devices(): Promise<{ self: string; devices: readonly Device[] }>;
  remove(id: string): Promise<void>;
  /** startPairing opens a pairing for a device listed as name ("": its own); it returns what ends it. */
  startPairing(name: string, listener: (event: StreamEvent<StartPairingResponse>) => void): () => void;
  confirm(token: string, accept: boolean): Promise<void>;
  /** uri is a pairing's ticket as the kith://pair URI a device joins with (the relay's PairingURI). */
  uri(ticket: NonNullable<Ticket>): Promise<string>;
  /** qr is data as a QR code's modules (the relay's QRCode). */
  qr(data: string): Promise<Uint8Array>;
  /** join has the relay join the bridge uri names, as kith join does; it returns what ends it. */
  join(uri: string, name: string, listener: (event: StreamEvent<JoinBridgeResponse>) => void): () => void;
}

type Ticket = Extract<StartPairingResponse["event"], { case: "ticket" }>["value"] | undefined;

/** A pairing under way, as the window draws it. */
export interface PairingView {
  /** The URI the new device joins with, and its QR code, once kithd gave the ticket. */
  uri: string;
  qr: QR | undefined;
  expires: Date | undefined;
  /** The device that answered, to be confirmed. */
  pending: { name: string; id: string; digits: string; role: string } | undefined;
  /** Main is asking the person, or kithd is pairing it. */
  confirming: boolean;
  /** How it ended; "" while it runs. */
  ended: "" | "paired" | "refused" | "expired" | "abandoned" | "failed";
  /** Why it failed, or what the last answer said. */
  note: string;
}

/** A join under way, as the window draws it. */
export interface JoinView {
  stage: "asking" | "digits" | "joined" | "failed";
  digits: string;
  /** The bridge joined, as device.toml names it. */
  bridge: string;
  note: string;
}

/** The settings page's devices section. */
export interface DevicesView {
  /** kithd's own device ID, and the devices paired; undefined until read. */
  self: string;
  list: readonly Device[] | undefined;
  /** What the section says: the last unpairing's outcome, or a failure. */
  note: string;
  /** The device being unpaired ("" none). */
  unpairing: string;
  pairing: PairingView | undefined;
  join: JoinView | undefined;
}

export const noDevices: DevicesView = {
  self: "",
  list: undefined,
  note: "",
  unpairing: "",
  pairing: undefined,
  join: undefined,
};

/**
 * said is a failure as the window says it: without the IPC's and Connect's wrapping
 * ("Error invoking remote method…", "[failed_precondition]") or the gate's "kith:".
 */
export function said(err: unknown): string {
  const text = err instanceof Error ? err.message : String(err);
  return text
    .replace(/^Error invoking remote method '[^']*': /, "")
    .replace(/^(Connect)?Error: /, "")
    .replace(/^\[[a-z_]+\] /, "")
    .replace(/^kith: /, "");
}

/** notListening: kithd refused a pairing for listening nowhere a device can dial. */
export function notListening(note: string): boolean {
  return note.includes("[remote] listen");
}

const endings = new Map<PairingOutcome, PairingView["ended"]>([
  [PairingOutcome.CONFIRMED, "paired"],
  [PairingOutcome.REFUSED, "refused"],
  [PairingOutcome.EXPIRED, "expired"],
  [PairingOutcome.ABANDONED, "abandoned"],
]);

/**
 * Devices leads the section: one pairing and one join at a time, each answer for the one
 * the window shows or dropped (a pairing closed, a join dismissed meanwhile).
 */
export class Devices {
  private view: DevicesView = noDevices;
  /** Counts pairings and joins begun: an answer for an earlier one is dropped. */
  private pairs = 0;
  private joins = 0;
  private token = "";
  private stopPairing: () => void = () => undefined;
  private stopJoin: () => void = () => undefined;

  constructor(
    private readonly calls: DevicesCalls,
    private readonly changed: (view: DevicesView) => void,
  ) {}

  snapshot(): DevicesView {
    return this.view;
  }

  /** read reads the devices afresh. */
  read(): void {
    this.calls.devices().then(
      (r) => {
        this.set({ self: r.self, list: r.devices });
      },
      (err: unknown) => {
        this.set({ note: `Could not list the devices: ${said(err)}` });
      },
    );
  }

  /** unpair asks main to unpair device (it asks the person first), then reads the list again. */
  unpair(device: Device): void {
    if (this.view.unpairing !== "") {
      return;
    }
    const label = device.name || device.id;
    this.set({ unpairing: device.id, note: "" });
    this.calls.remove(device.id).then(
      () => {
        this.set({ unpairing: "", note: `Unpaired ${label}.` });
        this.read();
      },
      (err: unknown) => {
        this.set({ unpairing: "", note: `${label} was not unpaired: ${said(err)}` });
        this.read();
      },
    );
  }

  /** pair opens a pairing for a new device, listed as name ("": the name it gives); none while one is open. */
  pair(name: string): void {
    if (this.view.pairing !== undefined) {
      return;
    }
    const gen = ++this.pairs;
    this.token = "";
    this.set({
      pairing: {
        uri: "",
        qr: undefined,
        expires: undefined,
        pending: undefined,
        confirming: false,
        ended: "",
        note: "",
      },
    });
    this.stopPairing = this.calls.startPairing(name.trim(), (event) => {
      if (gen === this.pairs) {
        this.heard(gen, event);
      }
    });
  }

  /** confirm asks main to pair the device that answered: it asks the person, showing it. */
  confirm(): void {
    this.answer(true);
  }

  /** refuse ends the pairing: nothing is paired. */
  refuse(): void {
    this.answer(false);
  }

  /** closePairing ends the pairing shown, if it still runs: kithd pairs nothing. */
  closePairing(): void {
    this.pairs++;
    this.stopPairing();
    this.token = "";
    this.set({ pairing: undefined });
  }

  /** join asks main to join the bridge uri names (it asks the person first), listed there as name. */
  join(uri: string, name: string): void {
    if (this.view.join !== undefined && this.view.join.stage !== "failed") {
      return;
    }
    const gen = ++this.joins;
    this.set({ join: { stage: "asking", digits: "", bridge: "", note: "" } });
    this.stopJoin = this.calls.join(uri.trim(), name.trim(), (event) => {
      if (gen === this.joins) {
        this.joinHeard(event);
      }
    });
  }

  /** dismissJoin drops the join shown; one under way is cut, unconfirmed. */
  dismissJoin(): void {
    this.joins++;
    this.stopJoin();
    this.set({ join: undefined });
  }

  /** stop drops what runs with the window: main ends it. */
  stop(): void {
    this.pairs++;
    this.joins++;
  }

  private answer(accept: boolean): void {
    const p = this.view.pairing;
    if (p?.pending === undefined || p.confirming || p.ended !== "") {
      return;
    }
    const gen = this.pairs;
    this.setPairing({ confirming: true, note: "" });
    this.calls.confirm(this.token, accept).then(
      () => {
        if (gen === this.pairs) {
          this.setPairing({ confirming: false });
        }
      },
      (err: unknown) => {
        if (gen === this.pairs) {
          this.setPairing({ confirming: false, note: said(err) });
        }
      },
    );
  }

  private heard(gen: number, event: StreamEvent<StartPairingResponse>): void {
    switch (event.kind) {
      case "frames":
        for (const f of event.frames) {
          this.frame(gen, f);
        }
        return;
      case "ended":
      case "refused":
      case "detached":
        if (this.view.pairing?.ended === "") {
          this.setPairing({
            ended: "failed",
            confirming: false,
            note: event.error === "" ? "The pairing ended." : said(event.error),
          });
        }
        return;
      default:
        return;
    }
  }

  private frame(gen: number, f: StartPairingResponse): void {
    switch (f.event.case) {
      case "ticket": {
        const ticket = f.event.value;
        this.token = ticket.token;
        this.setPairing({ expires: ticket.expires ? new Date(Number(ticket.expires.seconds) * 1000) : undefined });
        this.calls
          .uri(ticket)
          .then(async (uri) => {
            if (gen !== this.pairs) {
              return;
            }
            this.setPairing({ uri });
            const dark = await this.calls.qr(uri);
            if (gen === this.pairs) {
              this.setPairing({ qr: { side: sideOf(dark), dark } });
            }
          })
          .catch((err: unknown) => {
            if (gen === this.pairs) {
              this.setPairing({ note: `The pairing's code could not be shown: ${said(err)}` });
            }
          });
        return;
      }
      case "pending": {
        const d = f.event.value;
        this.setPairing({ pending: { name: d.name, id: d.id, digits: d.digits, role: d.role } });
        return;
      }
      case "outcome": {
        const ended = endings.get(f.event.value) ?? "failed";
        this.setPairing({ ended, confirming: false });
        if (ended === "paired") {
          this.read();
        }
        return;
      }
      default:
        return;
    }
  }

  private joinHeard(event: StreamEvent<JoinBridgeResponse>): void {
    const j = this.view.join;
    if (j === undefined) {
      return;
    }
    switch (event.kind) {
      case "frames":
        for (const f of event.frames) {
          if (f.event.case === "digits") {
            this.set({ join: { ...j, stage: "digits", digits: f.event.value } });
          } else if (f.event.case === "joined") {
            this.set({ join: { ...j, stage: "joined", bridge: f.event.value.name } });
          }
        }
        return;
      case "ended":
      case "refused":
      case "detached":
        if (j.stage !== "joined") {
          this.set({
            join: { ...j, stage: "failed", note: event.error === "" ? "The join ended." : said(event.error) },
          });
        }
        return;
      default:
        return;
    }
  }

  private setPairing(change: Partial<PairingView>): void {
    const p = this.view.pairing;
    if (p !== undefined) {
      this.set({ pairing: { ...p, ...change } });
    }
  }

  private set(change: Partial<DevicesView>): void {
    this.view = { ...this.view, ...change };
    this.changed(this.view);
  }
}
