// A message's poll, drawn: its question, each answer with its count and share, this
// person's own marked. Open, an answer is chosen by pressing it, or several ticked and
// voted; who chose what is shown on asking. And the composer's form asking one.
import { useState } from "react";

import type { Message, Poll } from "../gen/backend/v1/types_pb";
import type { ComposerView } from "./compose";
import {
  asked,
  canTakeBack,
  choice,
  pollNote,
  pollTally,
  share,
  tick,
  ticked,
  voted,
  voterLines,
  voterName,
} from "./polls";
import type { Session } from "./session";

export function PollBlock({
  m,
  p,
  composer,
  session,
}: {
  m: Message;
  p: Poll;
  /** The open room's composer: undefined, the poll is only shown. */
  composer: ComposerView | undefined;
  session: Session;
}) {
  // The answers ticked in a poll that takes several, once the person ticks one.
  const [touched, setTouched] = useState<ReadonlySet<string> | undefined>(undefined);
  const ticks = ticked(p, touched);
  const open = composer !== undefined && !p.closed;
  const voters = composer?.voters?.event === m.id ? composer.voters : undefined;
  const vote = (options: readonly string[]) => {
    setTouched(undefined);
    void session.vote(m, options);
  };
  const note = pollNote(p);
  return (
    <div className="poll" role="group" aria-label="Poll">
      <p className="poll-question" dir="auto">
        📊 {p.question}
        {note !== "" && <span className="detail"> ({note})</span>}
      </p>
      <ul className="poll-options">
        {p.options.map((o) => {
          const face = (
            <>
              <span className="poll-answer" dir="auto">
                {p.multiple && open ? "" : o.mine ? "● " : "○ "}
                {o.text}
              </span>
              <span className="detail poll-tally">{pollTally(p, o)}</span>
              <progress className="poll-share" max={100} value={share(p, o)} aria-hidden="true" />
            </>
          );
          return (
            <li key={o.id}>
              {!open ? (
                <div className={o.mine ? "poll-option mine" : "poll-option"}>{face}</div>
              ) : p.multiple ? (
                <label className={o.mine ? "poll-option mine" : "poll-option"}>
                  <input
                    type="checkbox"
                    checked={ticks.has(o.id)}
                    onChange={() => {
                      setTouched(tick(ticks, o.id));
                    }}
                  />
                  {face}
                </label>
              ) : (
                <button
                  type="button"
                  className="poll-option"
                  aria-pressed={o.mine}
                  onClick={() => {
                    vote([o.id]);
                  }}
                >
                  {face}
                </button>
              )}
            </li>
          );
        })}
      </ul>
      {composer !== undefined && (
        <div className="poll-actions">
          {open && p.multiple && (
            <button
              type="button"
              disabled={ticks.size === 0 && !voted(p)}
              onClick={() => {
                vote(choice(p, ticks));
              }}
            >
              {ticks.size === 0 && voted(p) ? "Take my vote back" : "Vote"}
            </button>
          )}
          {canTakeBack(p) && (
            <button
              type="button"
              onClick={() => {
                vote([]);
              }}
            >
              Take my vote back
            </button>
          )}
          <button
            type="button"
            aria-expanded={voters !== undefined}
            onClick={() => {
              if (voters === undefined) {
                session.showVoters(m);
              } else {
                session.closeVoters();
              }
            }}
          >
            Who voted
          </button>
        </div>
      )}
      {voters !== undefined && composer !== undefined && (
        <ul className="poll-voters detail" aria-label="Who voted">
          {voterLines(p, voters, (v) => voterName(v, composer.selves)).map((line, i) => (
            <li key={i} dir="auto">
              {line}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

/** AskPoll is the composer's form for a poll: a question, its answers, and whether several may be chosen. */
export function AskPoll({ session, onClose }: { session: Session; onClose: () => void }) {
  const [question, setQuestion] = useState("");
  const [answers, setAnswers] = useState(["", ""]);
  const [multiple, setMultiple] = useState(false);
  const [busy, setBusy] = useState(false);
  const poll = asked(question, answers, multiple);
  return (
    <form
      className="ask-poll"
      aria-label="Ask a poll"
      onSubmit={(e) => {
        e.preventDefault();
        if (poll === undefined || busy) {
          return;
        }
        setBusy(true);
        void session.askPoll(poll).then((done) => {
          setBusy(false);
          if (done) {
            onClose();
          }
        });
      }}
    >
      <input
        dir="auto"
        aria-label="Question"
        placeholder="A question"
        value={question}
        onChange={(e) => {
          setQuestion(e.target.value);
        }}
      />
      {answers.map((a, i) => (
        <input
          key={i}
          dir="auto"
          aria-label={`Answer ${String(i + 1)}`}
          placeholder={`Answer ${String(i + 1)}`}
          value={a}
          onChange={(e) => {
            setAnswers(answers.map((x, j) => (j === i ? e.target.value : x)));
          }}
        />
      ))}
      <label>
        <input
          type="checkbox"
          checked={multiple}
          onChange={(e) => {
            setMultiple(e.target.checked);
          }}
        />{" "}
        Voters may choose several answers
      </label>
      <div className="ask-poll-actions">
        <button
          type="button"
          onClick={() => {
            setAnswers([...answers, ""]);
          }}
        >
          Add an answer
        </button>
        <button type="button" onClick={onClose}>
          Cancel
        </button>
        <button type="submit" className="send" disabled={poll === undefined || busy}>
          Create poll
        </button>
      </div>
    </form>
  );
}
