import { create } from "@bufbuild/protobuf";
import fc from "fast-check";
import { describe, expect, it } from "vitest";

import { BackendService, type LoginStep, LoginStepSchema } from "../gen/backend/v1/services_pb";
import { DeviceService } from "../gen/device/v1/device_pb";
import { LoginRefused, Logins, type Ticket } from "./logins";

const backend = BackendService.method;

/** The kinds of step kithd answers with; "gone" is a login kithd ended (another client began it again). */
type Kind = "ask" | "qr" | "configure" | "done" | "gone";

function stepOf(login: string, kind: Kind): LoginStep {
  switch (kind) {
    case "ask":
      return create(LoginStepSchema, { login, ask: [{ key: "code", label: "Code", secret: true }] });
    case "qr":
      return create(LoginStepSchema, { login, qr: "2@x", instead: "with a code instead" });
    case "configure":
      return create(LoginStepSchema, { login, configure: { table: "whatsapp.account", values: { name: login } } });
    case "done":
    case "gone":
      return create(LoginStepSchema, { login, done: "logged in" });
  }
}

/** What the window (or whatever runs in it) asks of a login, and what else happens. */
type Event =
  | { kind: "begin" }
  | { kind: "answer"; login: number; values: boolean }
  | { kind: "cancel"; login: number }
  | { kind: "instead"; login: number }
  | { kind: "write"; login: number }
  | { kind: "reply"; pick: number; step: Kind; fail: boolean }
  | { kind: "supersede"; login: number }
  | { kind: "restart" };

const event: fc.Arbitrary<Event> = fc.oneof(
  fc.constant({ kind: "begin" as const }),
  fc.record({ kind: fc.constant("answer" as const), login: fc.nat(3), values: fc.boolean() }),
  fc.record({ kind: fc.constant("cancel" as const), login: fc.nat(3) }),
  fc.record({ kind: fc.constant("instead" as const), login: fc.nat(3) }),
  fc.record({ kind: fc.constant("write" as const), login: fc.nat(3) }),
  fc.record({
    kind: fc.constant("reply" as const),
    pick: fc.nat(),
    step: fc.constantFrom<Kind>("ask", "qr", "configure", "done"),
    fail: fc.boolean(),
  }),
  fc.record({
    kind: fc.constant("reply" as const),
    pick: fc.nat(),
    step: fc.constantFrom<Kind>("ask", "qr", "configure"),
    fail: fc.constant(false),
  }),
  fc.record({ kind: fc.constant("supersede" as const), login: fc.nat(3) }),
  fc.constant({ kind: "restart" as const }),
);

/** A call admitted and on its way to kithd, answered when the test says. */
interface Flying {
  ticket: Ticket;
  login: string;
}

describe("the logins the window leads", () => {
  // Whatever the window asks, in whatever order kithd answers: no call of a login gets
  // past the gate unless the window leads it now (begun in this load, not canceled, not
  // ended as far as main heard); a step gets one answer at a time and its account one
  // write; and no login the window began is left running in kithd once nobody leads it
  // (the window reloaded, or closed).
  it("admit only the window's own live logins, and leave none running", async () => {
    await fc.assert(
      fc.asyncProperty(fc.array(event, { maxLength: 50, size: "max" }), async (events) => {
        const live = new Map<string, { load: number }>(); // kithd's logins the window began
        const dead = new Set<string>(); // ended, as main heard it, or canceled, or of an earlier load
        let load = 0;
        let n = 0;
        const logins = new Logins((login) => {
          live.delete(login);
          return Promise.resolve();
        });
        const flying: Flying[] = [];
        const answering = new Set<string>();
        const writing = new Set<string>();
        const named = (i: number) => `l${String(i)}`;
        const tryAdmit = (method: string, input: unknown): Ticket | undefined => {
          try {
            return logins.admit(method, input, {});
          } catch (err) {
            if (err instanceof LoginRefused) {
              return undefined;
            }
            throw err;
          }
        };
        for (const e of events) {
          switch (e.kind) {
            case "begin": {
              const t = tryAdmit(backend.beginLogin.name, create(backend.beginLogin.input, { network: "whatsapp" }));
              expect(t).toBeDefined();
              if (t) {
                flying.push({ ticket: t, login: "" });
              }
              break;
            }
            case "answer": {
              const login = named(e.login);
              const values: Record<string, string> = e.values ? { code: "123" } : {};
              const t = tryAdmit(backend.answerLogin.name, create(backend.answerLogin.input, { login, values }));
              if (t) {
                expect(dead.has(login), `an answer to ${login}, which the window no longer leads`).toBe(false);
                expect(answering.has(login), `a second answer to ${login}`).toBe(false);
                answering.add(login);
                flying.push({ ticket: t, login });
              }
              break;
            }
            case "cancel": {
              const login = named(e.login);
              const t = tryAdmit(backend.cancelLogin.name, create(backend.cancelLogin.input, { login }));
              if (t) {
                expect(dead.has(login), `canceled ${login}, which the window no longer leads`).toBe(false);
                live.delete(login);
                dead.add(login);
              }
              break;
            }
            case "instead": {
              const login = named(e.login);
              const t = tryAdmit(backend.insteadLogin.name, create(backend.insteadLogin.input, { login }));
              if (t) {
                expect(dead.has(login), `took ${login}'s other way, which the window no longer leads`).toBe(false);
              }
              break;
            }
            case "write": {
              const login = named(e.login);
              const write = DeviceService.method.writeAccount;
              const t = tryAdmit(
                write.name,
                create(write.input, { table: "whatsapp.account", values: { name: login } }),
              );
              if (t) {
                expect(dead.has(login), `wrote ${login}'s account, which the window no longer leads`).toBe(false);
                expect(writing.has(login), `${login}'s account written twice`).toBe(false);
                writing.add(login);
                flying.push({ ticket: t, login });
              }
              break;
            }
            case "reply": {
              const [f] = flying.splice(e.pick % Math.max(1, flying.length), 1);
              if (f === undefined) {
                break;
              }
              const method = f.ticket.method;
              if (method === backend.beginLogin.name) {
                const login = named(++n);
                const step = stepOf(login, e.step);
                if (e.step !== "done") {
                  live.set(login, { load: f.ticket.load });
                }
                if (f.ticket.load !== load || e.step === "done") {
                  dead.add(login);
                }
                logins.heard(f.ticket, create(backend.beginLogin.output, { step }));
              } else if (method === backend.answerLogin.name) {
                answering.delete(f.login);
                writing.delete(f.login);
                const gone = !live.has(f.login);
                if (gone || e.fail || e.step === "done") {
                  live.delete(f.login);
                  dead.add(f.login);
                  logins.heard(f.ticket, undefined, new Error("daemon: that login has ended; begin it again"));
                } else {
                  logins.heard(f.ticket, create(backend.answerLogin.output, { step: stepOf(f.login, e.step) }));
                }
              } else {
                if (e.fail) {
                  writing.delete(f.login);
                }
                logins.heard(f.ticket, {}, e.fail ? new Error("kithd away") : undefined);
              }
              break;
            }
            case "supersede":
              live.delete(named(e.login)); // the TUI began it again: kithd ended the window's
              break;
            case "restart":
              load++;
              for (const login of live.keys()) {
                dead.add(login);
              }
              answering.clear();
              writing.clear();
              await logins.restart();
              break;
          }
        }
        // Every answer in: no login of an earlier load runs; and once the window closes, none.
        while (flying.length > 0) {
          const f = flying.shift();
          if (f?.ticket.method === backend.beginLogin.name) {
            const login = named(++n);
            live.set(login, { load: f.ticket.load });
            logins.heard(f.ticket, create(backend.beginLogin.output, { step: stepOf(login, "qr") }));
          } else if (f) {
            logins.heard(f.ticket, undefined, new Error("kithd away"));
          }
        }
        await Promise.resolve();
        expect(
          [...live].filter(([, l]) => l.load !== load).map(([id]) => id),
          "a reloaded window's logins",
        ).toEqual([]);
        await logins.end();
        expect([...live.keys()], "a closed window's logins").toEqual([]);
      }),
      { numRuns: 1000 },
    );
  });

  it("refuse values a step did not ask, and the other way of a step that offers none", () => {
    const logins = new Logins(() => Promise.resolve());
    const t = logins.admit(backend.beginLogin.name, create(backend.beginLogin.input), {});
    logins.heard(t, create(backend.beginLogin.output, { step: stepOf("l1", "ask") }));
    const answer = (values: Record<string, string>) =>
      logins.admit(backend.answerLogin.name, create(backend.answerLogin.input, { login: "l1", values }), {});
    expect(() => answer({ password: "x" })).toThrow(LoginRefused);
    expect(() =>
      logins.admit(backend.insteadLogin.name, create(backend.insteadLogin.input, { login: "l1" }), {}),
    ).toThrow(LoginRefused);
    expect(() =>
      logins.admit(
        DeviceService.method.writeAccount.name,
        create(DeviceService.method.writeAccount.input, { table: "whatsapp.account", values: { name: "l1" } }),
        {},
      ),
    ).toThrow(LoginRefused);
    expect(answer({ code: "123" }).login).toBe("l1");
  });
});
