import { create } from "@bufbuild/protobuf";
import fc from "fast-check";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { type LoginNetwork, LoginNetworkSchema, type LoginStep, LoginStepSchema } from "../gen/backend/v1/services_pb";
import { Login, type LoginCalls, type LoginView, loginTimeout, qrPath, secretShown, sideOf } from "./login";

/** What a secret typed in a generated login is: it must never be in what the window shows. */
const secret = "hunter2-secret";

const whatsapp: LoginNetwork = create(LoginNetworkSchema, { network: "whatsapp", label: "WhatsApp" });

/** The kinds of step a generated kithd answers with, in the order its login goes through them. */
type Kind = "ask" | "qr" | "code" | "configure" | "done" | "restart" | "fail";

function stepOf(login: string, kind: Kind): LoginStep {
  switch (kind) {
    case "ask":
      return create(LoginStepSchema, {
        login,
        note: "",
        ask: [
          { key: "phone", label: "Phone", value: "+1555" },
          { key: "password", label: "Password", secret: true },
        ],
      });
    case "qr":
      return create(LoginStepSchema, { login, qr: "2@data", instead: "link with a code instead" });
    case "code":
      return create(LoginStepSchema, { login, code: "ABCD-1234", note: "type it on the phone" });
    case "configure":
      return create(LoginStepSchema, {
        login,
        account: "main",
        configure: { table: "whatsapp.account", values: { name: "main" } },
      });
    case "done":
      return create(LoginStepSchema, { login, done: "logged in to WhatsApp as main" });
    case "restart":
      return create(LoginStepSchema, { login, done: "set up", restart: true });
    case "fail":
      return create(LoginStepSchema, { login });
  }
}

/** A call kithd has not answered yet, answered when the test says. */
interface Pending {
  what: string;
  login: string;
  answer: () => void;
}

/**
 * Kithd is a model of kithd's logins (daemon.Logins) for the window's: each login goes
 * through its script a step per answer; a canceled one is ended at once, and a call of an
 * ended one fails, as kithd's do. Calls are answered only when the test answers them, in
 * whatever order it picks.
 */
class Kithd implements LoginCalls {
  readonly live = new Map<string, { at: number; asking: boolean }>();
  readonly pending: Pending[] = [];
  /** Answers sent to a step that asked while one was on its way already. */
  doubled = 0;
  answersInFlight = new Map<string, number>();
  written = 0;
  /** Calls the window made of a login after it canceled it, or of one it did not begin last. */
  strays = 0;
  /** The logins the window canceled; and the login each of its begins was answered with, in order. */
  private readonly canceled = new Set<string>();
  private readonly begun: string[] = [];
  private begins = 0;
  private n = 0;

  constructor(private readonly script: readonly Kind[]) {}

  networks(): Promise<LoginNetwork[]> {
    return Promise.resolve([whatsapp]);
  }

  begin(): Promise<LoginStep | undefined> {
    const call = this.begins++;
    return this.later("begin", "", () => {
      const login = `l${String(++this.n)}`;
      this.begun[call] = login;
      this.live.set(login, { at: 0, asking: false });
      return this.say(login);
    });
  }

  answer(login: string, values: Record<string, string>): Promise<LoginStep | undefined> {
    this.stray(login);
    const flying = (this.answersInFlight.get(login) ?? 0) + 1;
    this.answersInFlight.set(login, flying);
    if (flying > 1 && Object.keys(values).length > 0) {
      this.doubled++;
    }
    return this.later("answer", login, () => {
      this.answersInFlight.set(login, (this.answersInFlight.get(login) ?? 1) - 1);
      const l = this.live.get(login);
      if (l === undefined) {
        throw new Error("daemon: that login has ended; begin it again");
      }
      l.at++;
      return this.say(login);
    });
  }

  cancel(login: string): Promise<void> {
    this.canceled.add(login);
    this.live.delete(login);
    return Promise.resolve();
  }

  instead(login: string): Promise<void> {
    this.stray(login);
    return this.later("instead", login, () => {
      if (!this.live.has(login)) {
        throw new Error("daemon: that login has ended; begin it again");
      }
    });
  }

  writeAccount(): Promise<void> {
    return this.later("write", "", () => {
      this.written++;
    });
  }

  qr(): Promise<Uint8Array> {
    return this.later("qr", "", () => new Uint8Array([1, 0, 0, 1]));
  }

  /** stray counts a call of a login the window no longer leads, or did not begin last. */
  private stray(login: string): void {
    if (this.canceled.has(login) || login !== this.begun[this.begins - 1]) {
      this.strays++;
    }
  }

  /** say is the login's step now, ending it in kithd if the step does. */
  private say(login: string): LoginStep {
    const l = this.live.get(login);
    const kind = this.script[l?.at ?? 0] ?? "done";
    if (kind === "fail") {
      this.live.delete(login);
      throw new Error("the network refused the login");
    }
    if (kind === "done" || kind === "restart") {
      this.live.delete(login);
    }
    return stepOf(login, kind);
  }

  private later<T>(what: string, login: string, answer: () => T): Promise<T> {
    return new Promise<T>((resolve, reject) => {
      this.pending.push({
        what,
        login,
        answer: () => {
          try {
            resolve(answer());
          } catch (err) {
            reject(err instanceof Error ? err : new Error(String(err)));
          }
        },
      });
    });
  }
}

/** settle lets every answer given reach the window. */
async function settle(): Promise<void> {
  for (let i = 0; i < 10; i++) {
    await Promise.resolve();
  }
}

/** What the person (or the clock) does, between kithd's answers. */
type Act =
  | { kind: "begin" }
  | { kind: "submit"; leave: boolean }
  | { kind: "cancel" }
  | { kind: "instead" }
  | { kind: "timeout" }
  | { kind: "answer"; pick: number };

const act: fc.Arbitrary<Act> = fc.oneof(
  fc.constant({ kind: "begin" as const }),
  fc.record({ kind: fc.constant("submit" as const), leave: fc.boolean() }),
  fc.record({ kind: fc.constant("submit" as const), leave: fc.boolean() }),
  fc.constant({ kind: "cancel" as const }),
  fc.constant({ kind: "instead" as const }),
  fc.constant({ kind: "timeout" as const }),
  fc.record({ kind: fc.constant("answer" as const), pick: fc.nat() }),
  fc.record({ kind: fc.constant("answer" as const), pick: fc.nat() }),
  fc.record({ kind: fc.constant("answer" as const), pick: fc.nat() }),
);

const kinds: fc.Arbitrary<Kind[]> = fc
  .array(fc.constantFrom<Kind>("ask", "qr", "code", "configure"), { maxLength: 4 })
  .chain((steps) => fc.constantFrom<Kind>("done", "restart", "fail").map((end) => [...steps, end]));

describe("a login from the window", () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  // Whatever the person does and whatever order kithd answers in: the window never shows
  // a secret typed; a login it stopped leading (canceled, timed out) never comes back on
  // screen; it sends no second answer to a step while one is on its way; and once every
  // answer is in, kithd runs no login the window does not show (one canceled before kithd
  // named it included).
  it("never shows a secret, never revives a login it left, and leaves none running", async () => {
    await fc.assert(
      fc.asyncProperty(kinds, fc.array(act, { maxLength: 40, size: "max" }), async (script, acts) => {
        const kithd = new Kithd(script);
        let shown: LoginView = { networks: undefined, run: undefined, note: "" };
        let views: LoginView[] = [];
        const login = new Login(
          kithd,
          (v) => {
            shown = v;
            views.push(v);
          },
          () => undefined,
        );
        let leading = false;
        const check = () => {
          for (const v of views) {
            expect(JSON.stringify(v, (_k, x: unknown) => (x instanceof Uint8Array ? [...x] : x))).not.toContain(secret);
            if (!leading) {
              expect(v.run, "a login the window left came back").toBeUndefined();
            }
          }
          views = [];
          expect(kithd.doubled, "a second answer to a step").toBe(0);
          expect(kithd.strays, "a call of a login the window no longer leads").toBe(0);
        };
        for (const a of acts) {
          switch (a.kind) {
            case "begin":
              if (shown.run === undefined) {
                leading = true;
              }
              login.begin(whatsapp, "");
              break;
            case "submit":
              login.submit(a.leave ? { phone: "+1555" } : { phone: "+1555", password: secret });
              break;
            case "cancel":
              leading = false;
              login.cancel();
              break;
            case "instead":
              login.instead();
              break;
            case "timeout":
              if (shown.run !== undefined) {
                leading = false;
              }
              vi.advanceTimersByTime(loginTimeout);
              break;
            case "answer": {
              const [p] = kithd.pending.splice(a.pick % Math.max(1, kithd.pending.length), 1);
              p?.answer();
              break;
            }
          }
          await settle();
          if (shown.run === undefined) {
            leading = false; // it ended: the next begin leads again
          }
          check();
        }
        login.cancel();
        leading = false;
        while (kithd.pending.length > 0) {
          kithd.pending.shift()?.answer();
          await settle();
        }
        check();
        expect([...kithd.live.keys()], "logins left running in kithd").toEqual([]);
      }),
      { numRuns: 1000 },
    );
  });

  it("asks each field, sends the answers once, and shows a secret as dots", async () => {
    const kithd = new Kithd(["ask", "configure", "done"]);
    let shown: LoginView | undefined;
    let loggedIn = false;
    const login = new Login(
      kithd,
      (v) => (shown = v),
      () => (loggedIn = true),
    );
    login.begin(whatsapp, "");
    kithd.pending.shift()?.answer();
    await settle();
    expect(shown?.run?.ask.map((f) => f.key)).toEqual(["phone", "password"]);

    login.submit({ phone: " +1555 ", password: "" });
    expect(shown?.run?.note).toBe("Password is needed");
    expect(kithd.pending).toHaveLength(0);

    login.submit({ phone: "+1555", password: secret });
    expect(shown?.run?.answered).toEqual([
      { key: "phone", label: "Phone", shown: "+1555" },
      { key: "password", label: "Password", shown: secretShown },
    ]);
    expect(shown?.run?.busy).toBe(true);
    kithd.pending.shift()?.answer(); // the account to write
    await settle();
    expect(shown?.run?.account).toBe("main");
    kithd.pending.shift()?.answer(); // written
    await settle();
    expect(kithd.written).toBe(1);
    kithd.pending.shift()?.answer(); // done
    await settle();
    expect(shown?.run).toBeUndefined();
    expect(shown?.note).toBe("logged in to WhatsApp as main");
    expect(loggedIn).toBe(true);
  });

  it("says kithd must restart, as the TUI does attached to a bridge", async () => {
    const kithd = new Kithd(["restart"]);
    let shown: LoginView | undefined;
    const login = new Login(
      kithd,
      (v) => (shown = v),
      () => undefined,
    );
    login.begin(whatsapp, "");
    kithd.pending.shift()?.answer();
    await settle();
    expect(shown?.note).toBe("Could not sign in to WhatsApp: set up: restart kithd, then sign in again.");
  });

  it("draws a QR step's code, and takes the other way it offers once", async () => {
    const kithd = new Kithd(["qr", "done"]);
    let shown: LoginView | undefined;
    const login = new Login(
      kithd,
      (v) => (shown = v),
      () => undefined,
    );
    login.begin(whatsapp, "");
    kithd.pending.shift()?.answer();
    await settle();
    expect(kithd.pending.map((p) => p.what).sort()).toEqual(["answer", "qr"]);
    kithd.pending.find((p) => p.what === "qr")?.answer();
    await settle();
    expect(shown?.run?.qr).toEqual({ side: 2, dark: new Uint8Array([1, 0, 0, 1]) });
    expect(shown?.run?.instead).toBe("link with a code instead");
    login.instead();
    login.instead();
    expect(kithd.pending.filter((p) => p.what === "instead")).toHaveLength(1);
  });
});

describe("a QR code", () => {
  it("is a unit square per dark module, inside its quiet zone", () => {
    const dark = new Uint8Array([1, 0, 0, 1]);
    expect(sideOf(dark)).toBe(2);
    expect(qrPath({ side: 2, dark }, 4)).toBe("M4 4h1v1h-1zM5 5h1v1h-1z");
  });
});
