// The logins the window leads, as main sees their calls pass the gate. kithd runs a
// login as a conversation (BackendService's BeginLogin, AnswerLogin, CancelLogin,
// InsteadLogin): this holds the ones the window began and has not seen end, so the
// window answers, cancels or takes the other way only of its own, only as the step it
// was shown allows, one answer at a time; and so a login nobody leads any more (the
// window reloaded or closed) is canceled rather than left running in kithd until its
// own deadline. Answers are never kept or logged here: only which fields a step asked.
import type { MessageShape } from "@bufbuild/protobuf";

import { BackendService, type LoginStep } from "../gen/backend/v1/services_pb";
import { DeviceService } from "../gen/device/v1/device_pb";

const backend = BackendService.method;

/** A refusal: the window asked for what a login it leads does not allow. */
export class LoginRefused extends Error {}

/** One login the window leads: its last step, and what is under way of it. */
interface Held {
  step: LoginStep;
  /** What kithd's calls for it carry: the profile the window ran in when it began. */
  header: HeadersInit;
  /** An AnswerLogin is on its way: the window waits for the next step. */
  answering: boolean;
  /** The configure step's account is being written, or was: it is written once. */
  writing: boolean;
}

/** A call admitted, for what its answer says (heard). */
export interface Ticket {
  method: string;
  /** The window load it was made in. */
  load: number;
  /** The login it is about (none for BeginLogin), and what a login begun carries. */
  held: Held | undefined;
  login: string;
  header: HeadersInit;
}

/** ended is whether a step ends its login: done, failed, or kithd must restart first. */
export function ended(step: LoginStep): boolean {
  return step.login === "" || step.done !== "" || step.restart;
}

/** The calls of a login (and its configure step's write) that Logins scopes. */
export const loginCalls = new Set([
  backend.beginLogin.name,
  backend.answerLogin.name,
  backend.cancelLogin.name,
  backend.insteadLogin.name,
  DeviceService.method.writeAccount.name,
]);

export class Logins {
  private readonly held = new Map<string, Held>();
  /** Counts the window's loads: a login begun in an earlier one is nobody's. */
  private load = 0;

  /** cancel has kithd end a login, carrying header. */
  constructor(private readonly cancel: (login: string, header: HeadersInit) => Promise<void>) {}

  /**
   * admit is the ticket for a call of a login, refused unless the window leads that
   * login and its step allows the call; header is what a login begun now carries.
   */
  admit(method: string, input: unknown, header: HeadersInit): Ticket {
    const ticket: Ticket = { method, load: this.load, held: undefined, login: "", header };
    switch (method) {
      case backend.answerLogin.name: {
        const r = input as MessageShape<typeof backend.answerLogin.input>;
        const held = this.mine(r.login);
        if (held.answering) {
          throw new LoginRefused("kith: an answer to this login is on its way already");
        }
        const asked = new Set(held.step.ask.map((f) => f.key));
        const unasked = Object.keys(r.values).filter((k) => !asked.has(k));
        if (unasked.length > 0) {
          throw new LoginRefused(`kith: the login's step does not ask ${unasked.join(", ")}`);
        }
        held.answering = true;
        return { ...ticket, held, login: r.login };
      }
      case backend.cancelLogin.name: {
        const r = input as MessageShape<typeof backend.cancelLogin.input>;
        const held = this.mine(r.login);
        this.held.delete(r.login); // whatever answer is still on its way, the window leads it no more
        return { ...ticket, held, login: r.login };
      }
      case backend.insteadLogin.name: {
        const r = input as MessageShape<typeof backend.insteadLogin.input>;
        const held = this.mine(r.login);
        if (held.step.qr === "" || held.step.instead === "") {
          throw new LoginRefused("kith: the login's step offers no other way");
        }
        return { ...ticket, held, login: r.login };
      }
      case DeviceService.method.writeAccount.name: {
        const r = input as MessageShape<typeof DeviceService.method.writeAccount.input>;
        const entry = [...this.held].find(([, h]) => sameRecord(h.step, r));
        if (entry === undefined) {
          throw new LoginRefused("kith: the window writes only the account a login it leads hands out");
        }
        const [login, held] = entry;
        if (held.writing || held.answering) {
          throw new LoginRefused("kith: that account is written already");
        }
        held.writing = true;
        return { ...ticket, held, login };
      }
    }
    return ticket;
  }

  /** heard is what kithd answered a ticket's call: response, or err when it failed. */
  heard(ticket: Ticket, response: unknown, err?: unknown): void {
    const held = ticket.held;
    if (ticket.method === backend.beginLogin.name) {
      const step = (response as MessageShape<typeof backend.beginLogin.output> | undefined)?.step;
      if (err !== undefined || step === undefined || ended(step)) {
        return;
      }
      if (ticket.load !== this.load) {
        void this.cancel(step.login, ticket.header).catch(() => undefined); // begun for a window since gone
        return;
      }
      this.held.set(step.login, { step, header: ticket.header, answering: false, writing: false });
      return;
    }
    if (held === undefined) {
      return;
    }
    switch (ticket.method) {
      case backend.answerLogin.name: {
        if (this.held.get(ticket.login) !== held) {
          return; // canceled, or the window reloaded, meanwhile: it stays ended
        }
        const step = (response as MessageShape<typeof backend.answerLogin.output> | undefined)?.step;
        if (err !== undefined || step === undefined || ended(step)) {
          // A failure ends the login: kithd's own did; one on the way (kithd or the relay
          // away) may not have, and nobody leads it now, so kithd is told to end it.
          this.held.delete(ticket.login);
          if (err !== undefined) {
            void this.cancel(ticket.login, held.header).catch(() => undefined);
          }
          return;
        }
        held.step = step;
        held.answering = false;
        held.writing = false;
        return;
      }
      case DeviceService.method.writeAccount.name:
        if (err !== undefined) {
          held.writing = false; // not written: the window may try again
        }
    }
  }

  /**
   * restart is the window loading afresh: the logins it led are nobody's, so kithd ends
   * them; a login begun before and answered after is ended as it arrives.
   */
  restart(): Promise<void> {
    this.load++;
    return this.end();
  }

  /** end has kithd end every login the window leads (it is closing), each canceled at most once. */
  end(): Promise<void> {
    const all = [...this.held];
    this.held.clear();
    return Promise.all(all.map(([login, h]) => this.cancel(login, h.header).catch(() => undefined))).then(
      () => undefined,
    );
  }

  /** mine is the login named, refused unless the window leads it. */
  private mine(login: string): Held {
    const held = this.held.get(login);
    if (held === undefined) {
      throw new LoginRefused("kith: the window may act only on a login it leads, and that one has ended");
    }
    return held;
  }
}

/** sameRecord is whether step's configure record is the one r writes. */
function sameRecord(step: LoginStep, r: { table: string; values: Record<string, string> }): boolean {
  const c = step.configure;
  if (c === undefined || c.table !== r.table) {
    return false;
  }
  const a = Object.keys(c.values);
  const b = Object.keys(r.values);
  return a.length === b.length && a.every((k) => Object.hasOwn(r.values, k) && r.values[k] === c.values[k]);
}
