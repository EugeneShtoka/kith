// The composer's sending later and its commands, drawn: the Later form (a clock time or
// a delay, read as the TUI's /at and /in), the send queue with cancel and try again,
// messages not queued, the links a command asks to choose from, and what a command or
// /topic gives to read. Their logic is compose.ts's, schedule.ts's and slash.ts's.
import { useState } from "react";

import type { ComposerView } from "./compose";
import { empty, type When } from "./schedule";
import type { Session } from "./session";

/** LaterForm asks when the composer's words go: at a clock time, or after a delay. */
export function LaterForm({ session, onClose }: { session: Session; onClose: () => void }) {
  const [kind, setKind] = useState<When>("at");
  const [when, setWhen] = useState("");
  return (
    <form
      className="later-form"
      aria-label="Send later"
      onSubmit={(e) => {
        e.preventDefault();
        if (when.trim() !== "") {
          void session.later(kind, when).then(onClose);
        }
      }}
    >
      <select
        aria-label="When"
        value={kind}
        onChange={(e) => {
          setKind(e.target.value === "in" ? "in" : "at");
        }}
      >
        <option value="at">At</option>
        <option value="in">In</option>
      </select>
      <input
        aria-label={kind === "at" ? "Time" : "Delay"}
        placeholder={kind === "at" ? "09:00 or 9pm" : "30m, 2h or 1h30m"}
        value={when}
        onChange={(e) => {
          setWhen(e.target.value);
        }}
      />
      <button type="submit" disabled={when.trim() === ""}>
        Schedule
      </button>
      <button
        type="button"
        onClick={() => {
          session.showQueue(false);
          onClose();
        }}
      >
        Queued here
      </button>
      <button type="button" aria-label="Close" onClick={onClose}>
        ×
      </button>
    </form>
  );
}

/** Queue is the send queue: this room's or every room's, each with cancel, and try again where it waits. */
export function Queue({ queue, session }: { queue: NonNullable<ComposerView["queue"]>; session: Session }) {
  const every = queue.room === "";
  return (
    <section className="queue" aria-label="Queued to send">
      <header>
        <strong>{every ? "Queued in every room" : "Queued in this room"}</strong>
        <button
          type="button"
          onClick={() => {
            session.showQueue(!every);
          }}
        >
          {every ? "This room" : "Every room"}
        </button>
        <button
          type="button"
          aria-label="Close"
          onClick={() => {
            session.closeQueue();
          }}
        >
          ×
        </button>
      </header>
      {queue.read && queue.rows.length === 0 && <p className="detail">{empty(queue.room)}</p>}
      <ul>
        {queue.rows.map((r) => (
          <li key={r.id} className={r.problem === "" ? undefined : `queued-${r.problem}`}>
            <span dir="auto">
              {every && <span className="detail">{r.where} · </span>}
              {r.summary}
            </span>
            {r.problem !== "" && (
              <span className="detail">
                {r.problem}: <span dir="auto">{r.reason}</span>
              </span>
            )}
            <span className="queue-actions">
              {r.problem !== "" && (
                <button type="button" onClick={() => void session.retryQueued(r.id)}>
                  Try again
                </button>
              )}
              <button type="button" onClick={() => void session.cancelQueued(r.id)}>
                Cancel
              </button>
            </span>
          </li>
        ))}
      </ul>
    </section>
  );
}

/** Unscheduled are the messages whose Schedule failed: queued again as they were, or dismissed. */
export function Unscheduled({ c, session }: { c: ComposerView; session: Session }) {
  return c.unscheduled.map((u) => (
    <p key={u.message.txnId} className="problem">
      Not queued: <span dir="auto">{u.message.body.split("\n")[0]}</span> ({u.error}){" "}
      <button type="button" onClick={() => void session.scheduleAgain(u)}>
        Queue again
      </button>{" "}
      <button
        type="button"
        onClick={() => {
          session.dismissUnscheduled(u);
        }}
      >
        Dismiss
      </button>
    </p>
  ));
}

/** Choosing asks which of a message's links a command is given. */
export function Choosing({ choosing, session }: { choosing: NonNullable<ComposerView["choosing"]>; session: Session }) {
  return (
    <div className="choosing" role="group" aria-label={`Which link for /${choosing.name}`}>
      <span className="detail">/{choosing.name} — which link?</span>
      {choosing.links.map((link, i) => (
        <button key={link} type="button" onClick={() => void session.chooseLink(i)}>
          <span dir="auto">{link}</span>
        </button>
      ))}
      <button
        type="button"
        aria-label="Close"
        onClick={() => {
          session.closeChoosing();
        }}
      >
        ×
      </button>
    </div>
  );
}

/** Reading is what a command printed for reading (output = "pager"), or a room's topic in full. */
export function Reading({ reading, session }: { reading: NonNullable<ComposerView["reading"]>; session: Session }) {
  return (
    <section className="reading" aria-label={reading.title}>
      <header>
        <strong dir="auto">{reading.title}</strong>
        <button
          type="button"
          aria-label="Close"
          onClick={() => {
            session.closeReading();
          }}
        >
          ×
        </button>
      </header>
      {reading.truncated && <p className="detail">More than fits: the first 8 KB.</p>}
      <pre dir="auto">{reading.body}</pre>
    </section>
  );
}
