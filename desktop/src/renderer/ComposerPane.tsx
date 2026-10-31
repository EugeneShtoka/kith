// The open room's composer, drawn: the words (enter sends, shift+enter breaks the line),
// what they reply to or correct, files offered, `@` mentions and `:shortcode:` emoji as
// they are typed, the emoji picker, a poll asked, the SMS question and sends that failed.
import { useEffect, useLayoutEffect, useRef, useState } from "react";

import type { Room } from "../gen/backend/v1/types_pb";
import type { ComposerView } from "./compose";
import { completions, shortcodeAt, shortcodeOf, toned } from "./emoji";
import { EmojiPicker } from "./EmojiPicker";
import { type Markup, marked } from "./markup";
import { matching, mentionAt, nameOf } from "./mentions";
import { AskPoll } from "./Poll";
import type { Session } from "./session";

/** How many completions the popup offers. */
const offered = 8;

/** The formatting bar's buttons: what each puts in, its name, and its face. */
const formats: [Markup, string, string][] = [
  ["bold", "Bold", "B"],
  ["italic", "Italic", "I"],
  ["code", "Code", "</>"],
  ["link", "Link", "🔗"],
];

/** A completion offered under the caret: what it shows, and what taking it does. */
interface Choice {
  key: string;
  label: string;
  detail: string;
  take: () => number;
}

export function ComposerPane({
  c,
  room,
  inThread,
  session,
}: {
  c: ComposerView;
  room: Room | undefined;
  /** A thread is open beside the room: what answers nothing goes into it. */
  inThread: boolean;
  session: Session;
}) {
  const area = useRef<HTMLTextAreaElement>(null);
  const [caret, setCaret] = useState(0);
  const [chosen, setChosen] = useState(0);
  const [picker, setPicker] = useState(false);
  const [asking, setAsking] = useState(false);
  const moveTo = useRef<number | undefined>(undefined);
  useLayoutEffect(() => {
    if (moveTo.current !== undefined) {
      area.current?.setSelectionRange(moveTo.current, moveTo.current);
      moveTo.current = undefined;
    }
  });

  const text = c.draft.body;
  const at = Math.min(caret, text.length);
  const mention = mentionAt(text, at);
  const code = mention === undefined ? shortcodeAt(text, at) : undefined;
  const wantsMembers = mention !== undefined;
  useEffect(() => {
    if (wantsMembers) {
      session.wantMembers();
    }
  }, [wantsMembers, session]);

  /** put replaces text[start, end) with what, the caret after it. */
  const put = (start: number, end: number, what: string) =>
    session.type(text.slice(0, start) + what + text.slice(end), start + what.length);
  const choices: Choice[] =
    mention !== undefined
      ? matching(c.members, mention.query).map((m) => ({
          key: m.userId,
          label: nameOf(m),
          detail: m.userId,
          take: () => session.mention(m, mention.start, at),
        }))
      : code !== undefined
        ? completions(c.emoji.set, code.query, c.emoji.scores, c.emoji.statics)
            .slice(0, offered)
            .map((e) => ({
              key: e,
              label: toned(e, c.emoji.tone),
              detail: `:${shortcodeOf(c.emoji.set, e)}:`,
              take: () => {
                session.emoji(toned(e, c.emoji.tone));
                return put(code.start, at, toned(e, c.emoji.tone));
              },
            }))
        : [];
  const pick = Math.min(chosen, Math.max(0, choices.length - 1));

  /** moved puts the caret at to, now and in the textarea once drawn. */
  const moved = (to: number) => {
    moveTo.current = to;
    setCaret(to);
    setChosen(0);
    area.current?.focus();
  };

  return (
    <section
      className="composer"
      aria-label="Composer"
      onDragOver={(e) => {
        e.preventDefault();
      }}
      onDrop={(e) => {
        e.preventDefault();
        void session.offer([...e.dataTransfer.files]);
      }}
    >
      {c.unsent.map((u) => (
        <p key={u.draft.txnId} className="problem">
          Not sent: <span dir="auto">{u.draft.body.split("\n")[0]}</span> ({u.error}){" "}
          <button type="button" onClick={() => void session.retry(u)}>
            Send again
          </button>{" "}
          <button
            type="button"
            onClick={() => {
              session.dismiss(u);
            }}
          >
            Dismiss
          </button>
        </p>
      ))}
      {c.asking && (
        <p className="banner waiting">
          Send this to <span dir="auto">{room?.name ?? c.asking.room}</span> as {c.asking.files > 0 ? "MMS" : "SMS"}? It
          goes through your phone&apos;s carrier.{" "}
          <button type="button" onClick={() => void session.answer(true)}>
            Send
          </button>{" "}
          <button type="button" onClick={() => void session.answer(false)}>
            Not now
          </button>
        </p>
      )}
      {inThread && c.draft.editing === "" && c.draft.replyTo === "" && <p className="detail">Writing in the thread</p>}
      {(c.draft.editing !== "" || c.draft.replyTo !== "") && (
        <p className="detail">
          {c.draft.editing !== "" ? "Correcting a message" : "Replying to a message"}{" "}
          <button
            type="button"
            aria-label="Cancel"
            onClick={() => {
              session.cancel();
            }}
          >
            ×
          </button>
        </p>
      )}
      {c.offered.length > 0 && (
        <ul className="offered" aria-label="Files to send">
          {c.offered.map((o) => (
            <li key={o.token}>
              <span dir="auto">{o.name}</span>{" "}
              <button type="button" aria-label={`Take ${o.name} back`} onClick={() => void session.discard(o.token)}>
                ×
              </button>
            </li>
          ))}
        </ul>
      )}
      {choices.length > 0 && (
        <ul className="completions" role="listbox">
          {choices.map((choice, i) => (
            <li key={choice.key} role="option" aria-selected={i === pick}>
              <button
                type="button"
                onMouseDown={(e) => {
                  e.preventDefault(); // the textarea keeps the focus
                  moved(choice.take());
                }}
              >
                <span dir="auto">{choice.label}</span> <span className="detail">{choice.detail}</span>
              </button>
            </li>
          ))}
        </ul>
      )}
      {picker && (
        <EmojiPicker
          emoji={c.emoji}
          kind="composed"
          onPick={(e) => {
            session.emoji(e);
            setPicker(false);
            moved(put(at, at, e));
          }}
          onClose={() => {
            setPicker(false);
          }}
        />
      )}
      {asking && (
        <AskPoll
          session={session}
          onClose={() => {
            setAsking(false);
          }}
        />
      )}
      <div className="compose-box">
        {c.markdown && (
          <div className="format-bar" role="toolbar" aria-label="Formatting">
            {formats.map(([markup, label, glyph]) => (
              <button
                key={markup}
                type="button"
                aria-label={label}
                title={label}
                className={`format-${markup}`}
                onMouseDown={(e) => {
                  e.preventDefault(); // the textarea keeps the focus
                }}
                onClick={() => {
                  const el = area.current;
                  if (el !== null) {
                    const f = marked(text, el.selectionStart, el.selectionEnd, markup);
                    moved(session.type(f.text, f.caret));
                  }
                }}
              >
                {glyph}
              </button>
            ))}
          </div>
        )}
        <textarea
          ref={area}
          dir="auto"
          rows={2}
          aria-label="Message"
          placeholder={c.draft.editing !== "" ? "The message, corrected" : "A message"}
          value={text}
          onChange={(e) => {
            const typedTo = e.target.selectionStart;
            const to = session.type(e.target.value, typedTo);
            if (to !== typedTo) {
              moveTo.current = to;
            }
            setCaret(to);
            setChosen(0);
          }}
          onSelect={(e) => {
            setCaret(e.currentTarget.selectionStart);
          }}
          onPaste={(e) => {
            const files = [...e.clipboardData.files];
            if (files.length > 0) {
              e.preventDefault();
              void session.offer(files);
            }
          }}
          onKeyDown={(e) => {
            const choice = choices[pick];
            if (choice !== undefined && (e.key === "ArrowDown" || e.key === "ArrowUp")) {
              e.preventDefault();
              setChosen((pick + (e.key === "ArrowDown" ? 1 : choices.length - 1)) % choices.length);
            } else if (choice !== undefined && (e.key === "Enter" || e.key === "Tab")) {
              e.preventDefault();
              moved(choice.take());
            } else if (e.key === "Enter" && !e.shiftKey) {
              e.preventDefault();
              void session.send();
            } else if (e.key === "Escape") {
              session.cancel();
            }
          }}
        />
        <div className="compose-actions">
          <button type="button" onClick={() => void session.pick()}>
            Attach
          </button>
          <button
            type="button"
            aria-expanded={picker}
            onClick={() => {
              setPicker(!picker);
            }}
          >
            Emoji
          </button>
          <button
            type="button"
            aria-expanded={asking}
            onClick={() => {
              setAsking(!asking);
            }}
          >
            Poll
          </button>
          <button type="button" className="send" onClick={() => void session.send()}>
            Send
          </button>
        </div>
      </div>
    </section>
  );
}
