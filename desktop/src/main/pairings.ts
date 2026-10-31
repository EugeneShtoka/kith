// Pairing, as main sees its calls pass the gate. Pairing grants a device full authority
// over the bridge, and joining one points this machine at another kithd: a window run by
// someone else's message must do neither on its own. So the window only asks; main asks
// the person, in a dialog of its own, and only their answer there sends ConfirmPairing,
// RemoveDevice or JoinBridge. Main confirms only a pairing the window opened in this load
// and profile and that is still open, showing the device that answered it; unpairs only
// a device the last Devices answer listed, saying so when it is the window's own; and
// joins only the bridge it showed, one join at a time, ended when the window reloads.
import type { Device, PendingDevice, StartPairingResponse } from "../gen/backend/v1/services_pb";
import type { InviteResponse, JoinBridgeResponse } from "../gen/device/v1/device_pb";

/** A refusal: the window asked for what its pairings do not allow. */
export class PairingRefused extends Error {}

/** What main asks the person, each in a dialog of its own; true is their yes. */
export interface Asks {
  /** pair: pair the device that answered (false refuses it). */
  pair(device: PendingDevice): Promise<boolean>;
  /** unpair: cut device off; own says it is this window's device (undefined: main could not tell). */
  unpair(device: Device, own: boolean | undefined): Promise<boolean>;
  /** join: join the bridge the pasted URI names. */
  join(invite: InviteResponse): Promise<boolean>;
}

/** One pairing the window opened (a StartPairing stream). */
export interface Run {
  readonly profile: string;
  readonly load: number;
  /** The token, once the ticket came. */
  token: string;
  /** The device that answered; spoiled: a second, other one came for the token. */
  pending: PendingDevice | undefined;
  spoiled: boolean;
  /** A confirmation is being asked or sent. */
  confirming: boolean;
  ended: boolean;
  /** stop ends the stream, and with it the pairing in kithd. */
  stop: () => void;
}

/** One join the window asked for (a JoinBridge stream, once main may open it). */
export interface Join {
  readonly load: number;
  /** The digits came: the bridge may have confirmed it by the time it is cut. */
  digits: boolean;
  joined: boolean;
  ended: boolean;
  stop: () => void;
}

/** shows: run's device is still the one shown, and the only one that answered. */
function shows(run: Run, shown: PendingDevice): boolean {
  return run.pending === shown && !run.spoiled;
}

export class Pairings {
  private load = 0;
  private profile = "";
  private readonly runs = new Set<Run>();
  /** The last Devices answer, in the load and profile it came in. */
  private listed: { load: number; profile: string; devices: Map<string, Device> } | undefined;
  private removing = false;
  private joining: Join | undefined;

  constructor(private readonly asks: Asks) {}

  /** runIn is the window running in profile: pairings and the list of another are not its own. */
  runIn(profile: string): void {
    if (profile === this.profile) {
      return;
    }
    this.profile = profile;
    this.listed = undefined;
    for (const run of this.runs) {
      this.end(run);
    }
  }

  /** restart is the window loading afresh: every pairing it opened ends, and a join under way. */
  restart(): void {
    this.load++;
    this.listed = undefined;
    for (const run of this.runs) {
      this.end(run);
    }
    this.joining?.stop();
  }

  /** opened is a StartPairing stream the window opens, one at a time; stop ends it. */
  opened(stop: () => void): Run {
    if ([...this.runs].some((r) => !r.ended)) {
      throw new PairingRefused("kith: the window has a pairing open already");
    }
    const run: Run = {
      profile: this.profile,
      load: this.load,
      token: "",
      pending: undefined,
      spoiled: false,
      confirming: false,
      ended: false,
      stop,
    };
    this.runs.add(run);
    return run;
  }

  /** frame is what kithd said of run's pairing: its ticket, the device that answered, how it ended. */
  frame(run: Run, frame: StartPairingResponse): void {
    if (run.ended) {
      return;
    }
    switch (frame.event.case) {
      case "ticket":
        run.token ||= frame.event.value.token;
        break;
      case "pending": {
        const p = frame.event.value;
        const was = run.pending;
        if (was === undefined) {
          run.pending = p;
        } else if (was.id !== p.id || was.digits !== p.digits || was.name !== p.name || was.role !== p.role) {
          run.spoiled = true; // which one would a confirmation pair? Neither.
        }
        break;
      }
      case "outcome":
        this.over(run); // kithd ends the stream after it
        break;
    }
  }

  /** end is run's pairing ended here: its stream is cut, and kithd ends the pairing. */
  end(run: Run): void {
    if (this.over(run)) {
      run.stop();
    }
  }

  /** over is run's pairing over, its token answered no more; false if it was already. */
  private over(run: Run): boolean {
    if (run.ended) {
      return false;
    }
    run.ended = true;
    this.runs.delete(run);
    return true;
  }

  /**
   * confirm answers the pairing under token. A refusal goes as asked; a yes only once the
   * person, shown the device that answered, says yes in main's dialog (their no refuses
   * it), and only while the pairing is still the one shown.
   */
  async confirm<R>(token: string, accept: boolean, send: (accept: boolean) => Promise<R>): Promise<R> {
    const run = this.live(token);
    if (!accept) {
      return this.answered(run, await send(false));
    }
    if (run.confirming) {
      throw new PairingRefused("kith: this pairing's confirmation is being asked already");
    }
    const shown = run.pending;
    if (shown === undefined || run.spoiled) {
      throw new PairingRefused("kith: no device waits to be confirmed in this pairing");
    }
    run.confirming = true;
    try {
      const yes = await this.asks.pair(shown);
      if (this.live(token) !== run || !shows(run, shown)) {
        throw new PairingRefused("kith: the pairing changed while it was asked about");
      }
      return this.answered(run, await send(yes));
    } finally {
      run.confirming = false;
    }
  }

  /** answered is run's pairing answered in kithd (it ends it): its token is spent here too. */
  private answered<R>(run: Run, answer: R): R {
    this.over(run);
    return answer;
  }

  /** devices keeps a Devices answer: the devices the window may ask to unpair. */
  devices(devices: readonly Device[], profile: string): void {
    if (profile === this.profile) {
      this.listed = { load: this.load, profile, devices: new Map(devices.map((d) => [d.id, d])) };
    }
  }

  /**
   * remove unpairs the device id, one at a time: only one the last Devices answer listed,
   * once the person says yes in main's dialog, told whether it is this window's own
   * (own's answer; one main cannot have is said as not known).
   */
  async remove<R>(id: string, own: () => Promise<string>, send: () => Promise<R>): Promise<R> {
    const listed = this.listed;
    const device = listed?.devices.get(id);
    if (listed === undefined || device === undefined || !this.current(listed)) {
      throw new PairingRefused(`kith: the window unpairs only a device it was shown listed, not ${id}`);
    }
    if (this.removing) {
      throw new PairingRefused("kith: a device is being unpaired already");
    }
    this.removing = true;
    try {
      const mine = await own().then(
        (self) => self === id,
        () => undefined,
      );
      if (!(await this.asks.unpair(device, mine))) {
        throw new PairingRefused(`kith: ${device.name || id} stays paired`);
      }
      if (!this.current(listed)) {
        throw new PairingRefused("kith: the window moved on while it was asked about");
      }
      const answer = await send();
      listed.devices.delete(id);
      return answer;
    } finally {
      this.removing = false;
    }
  }

  /**
   * join is a join the window asks for, one at a time: read is what the URI names, which
   * the person is shown; it resolves once they said yes in main's dialog, in the same
   * window load, to the join main then opens.
   */
  async join(read: () => Promise<InviteResponse>): Promise<Join> {
    if (this.joining !== undefined) {
      throw new PairingRefused("kith: a join is under way already");
    }
    const join: Join = { load: this.load, digits: false, joined: false, ended: false, stop: () => undefined };
    join.stop = () => {
      this.joinEnded(join, true);
    };
    this.joining = join;
    try {
      const invite = await read();
      if (!(await this.asks.join(invite))) {
        throw new PairingRefused("kith: not joined");
      }
      if (join.ended || join.load !== this.load) {
        throw new PairingRefused("kith: the window reloaded while the join was asked about");
      }
      return join;
    } catch (err) {
      this.joinEnded(join, false);
      throw err;
    }
  }

  /** joinFrame is what the relay said of join: the digits shown, then the bridge written. */
  joinFrame(join: Join, frame: JoinBridgeResponse): void {
    if (frame.event.case === "digits") {
      join.digits = true;
    } else if (frame.event.case === "joined") {
      join.joined = true;
    }
  }

  /**
   * joinEnded is join over, once: its stream ended, or was cut. It answers whether the
   * device file may name the bridge now, so the relay is to be started again: it was
   * written (joined), or the join was cut after the digits came, when the bridge may have
   * confirmed it already (a confirmation that came is written even so). A stream that
   * ended without joined wrote nothing.
   */
  joinEnded(join: Join, cut: boolean): boolean {
    if (join.ended) {
      return false;
    }
    join.ended = true;
    if (this.joining === join) {
      this.joining = undefined;
    }
    return join.joined || (cut && join.digits);
  }

  /** live is the pairing under token the window may answer: open, in this load and profile. */
  private live(token: string): Run {
    const run = [...this.runs].find((r) => r.token !== "" && r.token === token);
    if (run === undefined || run.ended || run.load !== this.load || run.profile !== this.profile) {
      throw new PairingRefused("kith: the window answers only a pairing it opened and that is still open");
    }
    return run;
  }

  /** current: listed is still the window's last list, in its load and profile. */
  private current(listed: { load: number; profile: string }): boolean {
    return this.listed === listed && listed.load === this.load && listed.profile === this.profile;
  }
}
