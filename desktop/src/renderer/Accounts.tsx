// The settings page's accounts: logging a network account in, or setting one up, as the
// TUI's :login does (login.ts leads it), and signing one out, as leaving an account's
// own space from the TUI's rail does.
import { type ReactNode, useState } from "react";

import type { RailRow } from "../gen/backend/v1/services_pb";
import { loginTitle, type LoginRun, type LoginView, type QR, qrPath } from "./login";
import type { Session } from "./session";

/** The quiet zone drawn around a QR code, in modules (the standard's four). */
const quiet = 4;

export function Accounts({ login, rail, session }: { login: LoginView; rail: readonly RailRow[]; session: Session }) {
  return (
    <>
      <h3>Accounts</h3>
      <p className="detail">
        Sign a network account in again, or set a new one up: the network leads the way, a step at a time.
      </p>
      {login.note && <p className="banner">{login.note}</p>}
      {login.run ? <Run run={login.run} session={session} /> : <Networks login={login} session={session} />}
      <SignOut rail={rail} session={session} />
    </>
  );
}

function Networks({ login, session }: { login: LoginView; session: Session }) {
  if (login.networks === undefined) {
    return <p className="banner">Reading the networks…</p>;
  }
  if (login.networks.length === 0) {
    return <p className="detail">kithd can log in to no network.</p>;
  }
  return (
    <ul className="networks">
      {login.networks.map((n) => (
        <li key={n.network}>
          <h4>{n.label}</h4>
          {n.detail && <p className="detail">{n.detail}</p>}
          <ul>
            {n.accounts.map((a) => (
              <li key={a.name} className="account">
                <span dir="auto">{a.name}</span>
                {a.detail && (
                  <span className="detail" dir="auto">
                    {a.detail}
                  </span>
                )}
                <button
                  type="button"
                  onClick={() => {
                    session.beginLogin(n, a.name);
                  }}
                >
                  Sign in
                </button>
              </li>
            ))}
            <li className="account">
              <button
                type="button"
                onClick={() => {
                  session.beginLogin(n, "");
                }}
              >
                New account
              </button>
            </li>
          </ul>
        </li>
      ))}
    </ul>
  );
}

/** Run is a login under way: its answers so far, then what its step asks or shows. */
function Run({ run, session }: { run: LoginRun; session: Session }) {
  const cancel = (
    <button
      type="button"
      onClick={() => {
        session.cancelLogin();
      }}
    >
      Cancel
    </button>
  );
  return (
    <section className="login" aria-label="Sign in">
      <h4>Sign in: {loginTitle(run)}</h4>
      {run.answered.length > 0 && (
        <dl className="answered">
          {run.answered.map((a) => (
            <div key={a.key}>
              <dt>{a.label}</dt>
              <dd dir="auto">{a.shown}</dd>
            </div>
          ))}
        </dl>
      )}
      {run.ask.length > 0 ? (
        <Ask key={run.step} run={run} session={session} cancel={cancel} />
      ) : (
        <>
          {run.code && <p className="login-code">{run.code}</p>}
          <Paragraphs text={run.note} />
          {run.qr && <QRCode qr={run.qr} />}
          <p className="login-actions">
            {run.instead && (
              <button
                type="button"
                onClick={() => {
                  session.takeInstead();
                }}
              >
                {run.instead}
              </button>
            )}
            {cancel}
          </p>
        </>
      )}
    </section>
  );
}

/**
 * Ask is the step's fields. What is typed goes to kithd as it is sent, and a secret's
 * field is emptied then: the window keeps no answer.
 */
function Ask({ run, session, cancel }: { run: LoginRun; session: Session; cancel: ReactNode }) {
  return (
    <form
      className="login-ask"
      autoComplete="off"
      onSubmit={(e) => {
        e.preventDefault();
        const form = e.currentTarget;
        const data = new FormData(form);
        const values: Record<string, string> = {};
        for (const f of run.ask) {
          const v = data.get(f.key);
          values[f.key] = typeof v === "string" ? v : "";
        }
        for (const input of form.querySelectorAll<HTMLInputElement>("input[type=password]")) {
          input.value = "";
        }
        session.answerLogin(values);
      }}
    >
      {run.note && <p className="problem">{run.note}</p>}
      {run.ask.map((f, i) => (
        <div key={f.key} className="login-field">
          <label>
            {f.label}
            {f.optional && <span className="detail"> (may be left empty)</span>}
            <input
              name={f.key}
              type={f.secret ? "password" : "text"}
              defaultValue={f.value}
              autoFocus={i === 0}
              spellCheck={false}
            />
          </label>
          <Paragraphs text={f.help} />
        </div>
      ))}
      <p className="login-actions">
        <button type="submit">Continue</button>
        {cancel}
      </p>
    </form>
  );
}

/** QRCode draws a QR step's code for a phone to scan: dark on light whatever the theme, inside its quiet zone. */
function QRCode({ qr }: { qr: QR }) {
  const side = qr.side + 2 * quiet;
  return (
    <svg className="qr" viewBox={`0 0 ${String(side)} ${String(side)}`} role="img" aria-label="A QR code to scan">
      <rect className="qr-light" width={side} height={side} />
      <path className="qr-dark" d={qrPath(qr, quiet)} />
    </svg>
  );
}

/** Paragraphs is text as its paragraphs (split at blank lines), as the TUI draws a field's help. */
function Paragraphs({ text }: { text: string }) {
  if (text.trim() === "") {
    return null;
  }
  return (
    <>
      {text
        .split(/\n\s*\n/)
        .filter((p) => p.trim() !== "")
        .map((p, i) => (
          <p key={i} className="detail" dir="auto">
            {p}
          </p>
        ))}
    </>
  );
}

/**
 * SignOut is each account's own space on the rail, signed out of as the TUI's rail leaves
 * one: its session ends, and signing in brings it back; first asked whether its rooms and
 * their history go too.
 */
function SignOut({ rail, session }: { rail: readonly RailRow[]; session: Session }) {
  const [asking, setAsking] = useState<string | undefined>(undefined);
  const [forget, setForget] = useState(false);
  const accounts = rail.filter((r) => r.owner !== "" && r.space !== "");
  if (accounts.length === 0) {
    return null;
  }
  return (
    <>
      <h4>Signed in</h4>
      <ul className="networks">
        {accounts.map((r) => (
          <li key={r.space} className="account">
            <span dir="auto">{r.label}</span>
            {asking === r.space ? (
              <span className="sign-out">
                <span>Sign out of {r.label}? Its session ends; signing in brings it back.</span>
                <label>
                  <input
                    type="checkbox"
                    checked={forget}
                    onChange={(e) => {
                      setForget(e.target.checked);
                    }}
                  />{" "}
                  Also forget its {new Set(r.rooms).size} rooms and their history here
                </label>
                <button
                  type="button"
                  onClick={() => {
                    setAsking(undefined);
                    void session.signOut(r.space, r.label, forget);
                  }}
                >
                  Sign out
                </button>
                <button
                  type="button"
                  onClick={() => {
                    setAsking(undefined);
                  }}
                >
                  Keep it
                </button>
              </span>
            ) : (
              <button
                type="button"
                onClick={() => {
                  setForget(false);
                  setAsking(r.space);
                }}
              >
                Sign out
              </button>
            )}
          </li>
        ))}
      </ul>
    </>
  );
}
