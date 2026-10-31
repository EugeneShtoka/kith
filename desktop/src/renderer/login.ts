// Logging a network account in, as the TUI's :login does (internal/tui/login.go): kithd
// lists the networks (LoginNetworks) and each leads its own login, a step at a time:
// fields to ask, a code or a QR code to show while the phone is used, a new account to
// write into the config, or the end. Nothing here knows a network. An answer goes
// straight to kithd and is not kept: what the window shows of one typed is its label,
// and a secret's as dots, whatever its length.
import type { LoginField, LoginNetwork, LoginRecord, LoginStep } from "../gen/backend/v1/services_pb";

/** How long a login waits, most of it for an answer, before it gives up (the TUI's loginTimeout). */
export const loginTimeout = 15 * 60 * 1000;

/** What a secret answer shows as. */
export const secretShown = "••••••";

/** What Login reaches of kithd (its login calls, through main) and of the relay. */
export interface LoginCalls {
  networks(): Promise<LoginNetwork[]>;
  begin(network: string, account: string): Promise<LoginStep | undefined>;
  answer(login: string, values: Record<string, string>): Promise<LoginStep | undefined>;
  cancel(login: string): Promise<void>;
  instead(login: string): Promise<void>;
  /** writeAccount writes a configure step's new account into the config (the relay's WriteAccount). */
  writeAccount(record: LoginRecord): Promise<void>;
  /** qr is data as a QR code's modules, a byte each (the relay's QRCode). */
  qr(data: string): Promise<Uint8Array>;
}

/** An answer as the window shows it: a secret as dots. key is its field's. */
export interface Answered {
  key: string;
  label: string;
  shown: string;
}

/** A QR code to draw: its side, and a byte per module (1 dark). */
export interface QR {
  side: number;
  dark: Uint8Array;
}

/** A login under way, as the window draws it. */
export interface LoginRun {
  network: LoginNetwork;
  /** The account, once known; "" sets a new one up. */
  account: string;
  /** Counts the steps, so each step's form starts afresh. */
  step: number;
  /** The fields this step asks; empty while kithd is at work. */
  ask: readonly LoginField[];
  answered: readonly Answered[];
  /** A code to type elsewhere; a QR code for a phone to scan, and another way it offers. */
  code: string;
  qr: QR | undefined;
  instead: string;
  /** What the network said last, or what the window is doing. */
  note: string;
  /** kithd is at work: the window waits for the next step. */
  busy: boolean;
}

/** The accounts section's login state. */
export interface LoginView {
  /** The networks kithd can log in to; undefined until read. */
  networks: readonly LoginNetwork[] | undefined;
  run: LoginRun | undefined;
  /** What the section says: how the last login ended, or a failure. */
  note: string;
}

/** title is what the login is called, as the TUI's pane title says it. */
export function loginTitle(run: LoginRun): string {
  return run.account === "" ? `${run.network.label} — a new account` : `${run.network.label} ${run.account}`;
}

/** sideOf is a QR code's side, from its modules (a square). */
export function sideOf(dark: Uint8Array): number {
  return Math.round(Math.sqrt(dark.length));
}

/**
 * qrPath is a QR code's dark modules as one SVG path, each a unit square, offset by the
 * quiet zone around it.
 */
export function qrPath(qr: QR, quiet: number): string {
  let d = "";
  for (let y = 0; y < qr.side; y++) {
    for (let x = 0; x < qr.side; x++) {
      if (qr.dark[y * qr.side + x] === 1) {
        d += `M${String(x + quiet)} ${String(y + quiet)}h1v1h-1z`;
      }
    }
  }
  return d;
}

/** over is whether a step ends its login: done, or kithd must restart first. */
function over(step: LoginStep): boolean {
  return step.login === "" || step.done !== "" || step.restart;
}

/** errorText is what the window says of a failure. */
function errorText(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

/**
 * Login leads one login at a time from the window. Every answer kithd sends is for the
 * login the window shows, or dropped: a login canceled, timed out or ended meanwhile
 * stays ended, and one begun and canceled before kithd named it is canceled once it is.
 */
export class Login {
  private view: LoginView = { networks: undefined, run: undefined, note: "" };
  /** Counts logins begun and ended: an answer for an earlier one is dropped. */
  private gen = 0;
  /** The login under way, as kithd names it ("" until it does). */
  private login = "";
  private timer: ReturnType<typeof setTimeout> | undefined;
  /** The QR codes asked of the relay: only the last one's answer is drawn. */
  private qrs = 0;

  constructor(
    private readonly calls: LoginCalls,
    /** changed hears each new view; done that a login ended having logged in. */
    private readonly changed: (view: LoginView) => void,
    private readonly done: () => void,
  ) {}

  snapshot(): LoginView {
    return this.view;
  }

  /** read reads the networks kithd can log in to, afresh. */
  read(): void {
    this.calls.networks().then(
      (networks) => {
        this.set({ networks });
      },
      (err: unknown) => {
        this.set({ note: `Could not list the networks: ${errorText(err)}` });
      },
    );
  }

  /** begin has kithd log account in on network ("" sets a new one up); none while another runs. */
  begin(network: LoginNetwork, account: string): void {
    if (this.view.run !== undefined) {
      return;
    }
    const gen = ++this.gen;
    this.login = "";
    this.timer = setTimeout(() => {
      this.abandon(`It waited ${String(loginTimeout / 60000)} minutes and gave up; start it again.`);
    }, loginTimeout);
    this.set({
      note: "",
      run: {
        network,
        account,
        step: 0,
        ask: [],
        answered: [],
        code: "",
        qr: undefined,
        instead: "",
        note: "Signing in…",
        busy: true,
      },
    });
    this.calls.begin(network.network, account).then(
      (step) => {
        if (gen !== this.gen && step !== undefined && !over(step)) {
          this.calls.cancel(step.login).catch(() => undefined); // canceled before kithd named it
        }
        this.step(gen, step);
      },
      (err: unknown) => {
        this.fail(gen, err);
      },
    );
  }

  /**
   * submit answers the fields the step asks with values, by key: a required one left
   * empty is asked again, and nothing is sent. The values go to kithd, and are not kept.
   */
  submit(values: Readonly<Record<string, string>>): void {
    const run = this.view.run;
    if (run === undefined || run.ask.length === 0) {
      return;
    }
    const answers: Record<string, string> = {};
    const answered = [...run.answered];
    for (const f of run.ask) {
      const value = (values[f.key] ?? "").trim();
      if (value === "" && !f.optional) {
        this.set({ run: { ...run, note: `${f.label} is needed` } });
        return;
      }
      answers[f.key] = value;
      answered.push({ key: f.key, label: f.label, shown: f.secret ? secretShown : value });
    }
    const gen = this.gen;
    this.set({ run: { ...run, ask: [], answered, note: "Signing in…", busy: true } });
    this.next(gen, this.calls.answer(this.login, answers));
  }

  /** instead takes the other way the QR code on screen offers. */
  instead(): void {
    const run = this.view.run;
    if (run?.qr === undefined || run.instead === "") {
      return;
    }
    const gen = this.gen;
    this.set({ run: { ...run, instead: "", note: "Taking the other way…" } });
    this.calls.instead(this.login).catch((err: unknown) => {
      const now = this.view.run;
      if (gen === this.gen && now !== undefined) {
        this.set({ run: { ...now, note: `Could not sign in another way: ${errorText(err)}` } });
      }
    });
  }

  /** cancel abandons the login: kithd ends it, and whatever it answers after is dropped. */
  cancel(): void {
    this.abandon("Sign-in canceled.");
  }

  /** abandon ends the login under way, saying note: kithd ends it, and what it answers after is dropped. */
  private abandon(note: string): void {
    if (this.view.run === undefined) {
      return;
    }
    const login = this.login;
    this.end(this.gen, note);
    if (login !== "") {
      this.calls.cancel(login).catch(() => undefined); // ended already, or kithd gone: it ends on its own
    }
  }

  /** say has the section say note (a sign-out's outcome). */
  say(note: string): void {
    this.set({ note });
  }

  /** stop drops the login with the window: main ends it in kithd. */
  stop(): void {
    this.gen++;
    clearTimeout(this.timer);
  }

  /** next waits for the step kithd answers with, and does what it says. */
  private next(gen: number, answer: Promise<LoginStep | undefined>): void {
    answer.then(
      (step) => {
        this.step(gen, step);
      },
      (err: unknown) => {
        this.fail(gen, err);
      },
    );
  }

  /** step does what kithd's step says: ask, show, write, or end. */
  private step(gen: number, step: LoginStep | undefined): void {
    if (gen !== this.gen) {
      return;
    }
    const run = this.view.run;
    if (run === undefined || step === undefined) {
      this.fail(gen, new Error("kithd answered no step"));
      return;
    }
    this.login = step.login;
    const account = step.account || run.account;
    const label = run.network.label;
    if (step.restart) {
      this.end(
        gen,
        `Could not sign in to ${label}: ${step.done || "kithd must restart"}: restart kithd, then sign in again.`,
      );
      return;
    }
    if (step.done !== "") {
      this.end(gen, step.done);
      this.done();
      return;
    }
    const shown: LoginRun = { ...run, account, step: run.step + 1, ask: [], code: "", qr: undefined, instead: "" };
    if (step.code !== "" || step.qr !== "") {
      this.set({ run: { ...shown, code: step.code, instead: step.instead, note: step.note, busy: true } });
      if (step.qr !== "") {
        this.drawQR(gen, step.qr);
      }
      this.next(gen, this.calls.answer(step.login, {}));
      return;
    }
    if (step.configure !== undefined) {
      this.set({ run: { ...shown, note: "Setting the account up…", busy: true } });
      this.calls.writeAccount(step.configure).then(
        () => {
          if (gen === this.gen) {
            this.next(gen, this.calls.answer(step.login, {}));
          }
        },
        (err: unknown) => {
          this.fail(gen, err);
        },
      );
      return;
    }
    if (step.ask.length === 0) {
      this.fail(gen, new Error("kithd answered a step with nothing to do"));
      return;
    }
    // A field asked again was refused: its earlier answer goes, and the note says why.
    const asked = new Set(step.ask.map((f) => f.key));
    this.set({
      run: {
        ...shown,
        ask: step.ask,
        answered: run.answered.filter((a) => !asked.has(a.key)),
        note: step.note,
        busy: false,
      },
    });
  }

  /** drawQR has the relay encode data, drawn while the step it came with is shown. */
  private drawQR(gen: number, data: string): void {
    const mine = ++this.qrs;
    this.calls.qr(data).then(
      (dark) => {
        const run = this.view.run;
        if (gen === this.gen && mine === this.qrs && run !== undefined) {
          this.set({ run: { ...run, qr: { side: sideOf(dark), dark } } });
        }
      },
      (err: unknown) => {
        const run = this.view.run;
        if (gen === this.gen && mine === this.qrs && run !== undefined) {
          this.set({ run: { ...run, note: `The QR code could not be drawn: ${errorText(err)}` } });
        }
      },
    );
  }

  private fail(gen: number, err: unknown): void {
    const run = this.view.run;
    if (gen === this.gen && run !== undefined) {
      this.end(gen, `Could not sign in to ${run.network.label}: ${errorText(err)}`);
    }
  }

  /** end ends the login gen, if it is the one under way, saying note. */
  private end(gen: number, note: string): void {
    if (gen !== this.gen) {
      return;
    }
    this.gen++;
    this.login = "";
    clearTimeout(this.timer);
    this.set({ run: undefined, note });
  }

  private set(change: Partial<LoginView>): void {
    this.view = { ...this.view, ...change };
    this.changed(this.view);
  }
}
