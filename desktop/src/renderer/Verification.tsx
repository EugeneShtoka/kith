// Verifying this session against another of your own, as the TUI's :verify does: a
// request to accept, then the emoji to compare on both, confirmed or rejected here.
// verify.ts holds the state; this draws it.
import type { Account } from "../gen/backend/v1/services_pb";
import type { Session } from "./session";
import { answerable, type Flow, type Verify, verifyAs } from "./verify";

/** VerifyStart is the settings page's way to ask this session's other sessions to verify it. */
export function VerifyStart({
  accounts,
  verify,
  session,
}: {
  accounts: readonly Account[];
  verify: Verify;
  session: Session;
}) {
  const as = verifyAs(accounts);
  const busy = verify.asking || verify.flow !== undefined;
  return (
    <>
      <h3>Verify this session</h3>
      <p className="detail">
        Have another of your sessions (Element, a phone) vouch for this one, so it can read your encrypted history: you
        compare emoji on both.
      </p>
      {as.length === 0 ? (
        <p className="detail">No Matrix account is connected.</p>
      ) : (
        <ul className="networks">
          {as.map((a) => (
            <li key={a.on} className="account">
              {as.length > 1 && <span dir="auto">{a.name}</span>}
              <button
                type="button"
                disabled={busy}
                onClick={() => {
                  session.verify(a.on);
                }}
              >
                {as.length > 1 ? "Verify" : "Verify this session"}
              </button>
            </li>
          ))}
        </ul>
      )}
    </>
  );
}

/**
 * VerifyDialog is a verification under way, or what it came to: over whatever the window
 * shows, since a request may arrive at any time.
 */
export function VerifyDialog({ verify, session }: { verify: Verify; session: Session }) {
  const f = verify.flow;
  if (f === undefined && !verify.asking && verify.said.length === 0) {
    return null;
  }
  return (
    <div className="verify-backdrop">
      <section className="verify" role="dialog" aria-modal="true" aria-label="Verify this session">
        <h2>Verify this session</h2>
        {f !== undefined ? (
          <Step f={f} session={session} />
        ) : verify.asking ? (
          <p className="banner">Asking your other sessions…</p>
        ) : (
          <>
            {verify.said.map((line) => (
              <p key={line}>{line}</p>
            ))}
            <p className="verify-actions">
              <button
                type="button"
                autoFocus
                onClick={() => {
                  session.dismissVerify();
                }}
              >
                Close
              </button>
            </p>
          </>
        )}
      </section>
    </div>
  );
}

function Step({ f, session }: { f: Flow; session: Session }) {
  const open = answerable(f);
  const cancel = (label: string) => (
    <button
      type="button"
      onClick={() => {
        session.cancelVerify();
      }}
    >
      {label}
    </button>
  );
  const answer = (label: string) => (
    <button
      type="button"
      className="primary"
      disabled={!open}
      onClick={() => {
        session.answerVerify();
      }}
    >
      {label}
    </button>
  );
  if (f.stage === "requested") {
    return (
      <>
        {f.ours ? (
          <p>Asked your other sessions to verify this one. Accept on one of them, then compare the emoji.</p>
        ) : (
          <p>
            <span dir="auto">{f.from}</span>
            {f.device && (
              <>
                {" "}
                (session <code>{f.device}</code>)
              </>
            )}{" "}
            wants to verify this session.
          </p>
        )}
        <Waiting f={f} next="the emoji" />
        <p className="verify-actions">
          {!f.ours && answer("Accept")}
          {cancel(f.ours ? "Cancel" : "Decline")}
        </p>
      </>
    );
  }
  return (
    <>
      <p>Compare these with what the other session shows. Do they match, in the same order?</p>
      {f.emojis.length > 0 && (
        <ol className="sas">
          {f.emojis.map((e, i) => (
            <li key={i}>
              <span className="sas-glyph" aria-hidden="true">
                {e.glyph}
              </span>
              <span className="sas-name">{e.name}</span>
            </li>
          ))}
        </ol>
      )}
      {f.decimals.length > 0 && (
        <p className={f.emojis.length > 0 ? "sas-numbers detail" : "sas-numbers"}>{f.decimals.map(String).join(" ")}</p>
      )}
      <Waiting f={f} next="the other session" />
      <p className="verify-actions">
        {answer("They match")}
        {cancel("They don't match")}
      </p>
    </>
  );
}

/** Waiting says why the step cannot be answered from here now, or why an answer failed. */
function Waiting({ f, next }: { f: Flow; next: string }) {
  if (f.sending) {
    return <p className="detail">Sending…</p>;
  }
  if (f.answered) {
    return (
      <p className="detail">
        {f.mine ? "Answered" : "Answered in another kith window"}: waiting for {next}.
      </p>
    );
  }
  if (f.failed) {
    return <p className="problem">That did not reach kithd: {f.failed}</p>;
  }
  return null;
}
