// The open room's composer, drawn: the words (enter sends, shift+enter breaks the line),
// what they reply to or correct, files offered, `@` mentions and `:shortcode:` emoji as
// they are typed, the emoji picker, a poll asked, the SMS question and sends that failed;
// the `/` commands as they are typed, sending later and the queue (Later.tsx).
// Under the words, a copy of them is drawn with the spelling marks and the completion's
// ghost text (Spelling.tsx): a textarea draws neither itself.
import { useEffect, useLayoutEffect, useRef, useState } from "react";

import type { Room } from "../gen/backend/v1/types_pb";
import { caretInRTL, wordBeingTyped } from "./complete";
import type { ComposerView } from "./compose";
import { completions, shortcodeAt, shortcodeOf, toned } from "./emoji";
import { EmojiPicker } from "./EmojiPicker";
import { Choosing, LaterForm, Queue, Reading, Unscheduled } from "./Later";
import { type Markup, marked } from "./markup";
import { matching, mentionAt, nameOf } from "./mentions";
import { AskPoll } from "./Poll";
import type { Session } from "./session";
import { offered as commandsFor } from "./slash";
import { carried } from "./spell";
import { Underlay, Walk, walkKey } from "./Spelling";

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
  const under = useRef<HTMLDivElement>(null);
  const [caret, setCaret] = useState(0);
  const [chosen, setChosen] = useState(0);
  const [picker, setPicker] = useState(false);
  const [asking, setAsking] = useState(false);
  const [later, setLater] = useState(false);
  /** The finished words are listed (Tab with none clearly leading). */
  const [listing, setListing] = useState(false);
  const moveTo = useRef<number | undefined>(undefined);
  /** The words as last drawn, and whether the last change of them was typed here. */
  const last = useRef(c.draft.body);
  const typed = useRef(false);
  useLayoutEffect(() => {
    const text = c.draft.body;
    if (moveTo.current !== undefined) {
      area.current?.setSelectionRange(moveTo.current, moveTo.current);
      moveTo.current = undefined;
    } else if (text !== last.current && !typed.current && document.activeElement === area.current) {
      // Changed by another writer (autocorrect, kithd folding in the assistant's words):
      // the caret stays where it was in the words, not at their end.
      const to = carried(last.current, text, caret);
      area.current?.setSelectionRange(to, to);
    }
    last.current = text;
    typed.current = false;
  });

  const text = c.draft.body;
  const at = Math.min(caret, text.length);
  const walk = c.spell.walk;
  const mention = mentionAt(text, at);
  const code = mention === undefined ? shortcodeAt(text, at) : undefined;
  const slash = mention === undefined && code === undefined ? commandsFor(text, at, c.commands) : undefined;
  const wantsMembers = mention !== undefined;
  useEffect(() => {
    if (wantsMembers) {
      session.wantMembers();
    }
  }, [wantsMembers, session]);
  // The commands folder is read as the `/` list opens, as the TUI scans it.
  const wantsCommands = slash !== undefined;
  useEffect(() => {
    if (wantsCommands) {
      session.wantCommands();
    }
  }, [wantsCommands, session]);

  /** put replaces text[start, end) with what, the caret after it. */
  const put = (start: number, end: number, what: string) =>
    session.type(text.slice(0, start) + what + text.slice(end), start + what.length);
  const atEnd = at === text.length && walk === undefined;
  const words = atEnd && listing ? c.assist.words : [];
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
        : slash !== undefined
          ? slash.map((s) => ({
              key: s.name,
              label: s.label,
              detail: s.detail,
              take: () => put(0, at, s.insert),
            }))
          : words.map((w, i) => ({
              key: w.word,
              label: w.word,
              detail: `alt+${String(i + 1)}`,
              take: () => {
                setListing(false);
                return session.chooseCompletion(i) ?? at;
              },
            }));
  const pick = Math.min(chosen, Math.max(0, choices.length - 1));
  // A command typed whole is done by enter, not completed again.
  const typedWhole = slash?.some((s) => s.name === text) ?? false;
  // The ghost and the next words, only at the draft's end with no other list open.
  const offer = atEnd && choices.length === 0 ? c.assist : undefined;

  /** moved puts the caret at to, now and in the textarea once drawn. */
  const moved = (to: number) => {
    moveTo.current = to;
    setCaret(to);
    setChosen(0);
    session.moved(to);
    area.current?.focus();
  };
  /** took moves the caret where a completion or a correction taken left it, if anything was. */
  const took = (to: number | undefined) => {
    if (to === undefined) {
      return false;
    }
    moved(to);
    return true;
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
      <Unscheduled c={c} session={session} />
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
      {c.note && <p className="detail">{c.note}</p>}
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
      {c.queue && <Queue queue={c.queue} session={session} />}
      {c.reading && <Reading reading={c.reading} session={session} />}
      {c.choosing && <Choosing choosing={c.choosing} session={session} />}
      {walk && <Walk walk={walk} session={session} took={took} />}
      {choices.length > 0 && (
        <ul className={slash === undefined ? "completions" : "completions commands"} role="listbox">
          {choices.map((choice, i) => (
            <li
              key={choice.key}
              role="option"
              aria-selected={i === pick}
              ref={
                i === pick && slash !== undefined
                  ? (el) => {
                      el?.scrollIntoView({ block: "nearest" }); // the long `/` list scrolls to the one chosen
                    }
                  : undefined
              }
            >
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
      {offer && offer.options.length >= 2 && (
        <div className="next-words" role="toolbar" aria-label="Next words">
          {offer.options.map((o, i) => (
            <button
              key={o}
              type="button"
              onMouseDown={(e) => {
                e.preventDefault(); // the textarea keeps the focus
                took(session.chooseCompletion(i));
              }}
            >
              <span className="detail">{i + 1}</span> <span dir="auto">{o}</span>
            </button>
          ))}
        </div>
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
      {later && (
        <LaterForm
          session={session}
          onClose={() => {
            setLater(false);
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
        <div className="compose-field">
          <Underlay
            ref={under}
            text={text}
            marks={c.spell.marks}
            settings={c.spell.settings}
            caret={walk === undefined ? at : -1}
            ghost={offer?.ghost ?? ""}
          />
          <textarea
            ref={area}
            dir="auto"
            rows={2}
            aria-label="Message"
            placeholder={c.draft.editing !== "" ? "The message, corrected" : "A message"}
            value={text}
            onScroll={(e) => {
              if (under.current !== null) {
                under.current.scrollTop = e.currentTarget.scrollTop;
              }
            }}
            onChange={(e) => {
              typed.current = true;
              const typedTo = e.target.selectionStart;
              const to = session.type(e.target.value, typedTo);
              if (to !== typedTo) {
                moveTo.current = to;
              }
              setCaret(to);
              setChosen(0);
              if (wordBeingTyped(e.target.value, to) === undefined) {
                setListing(false);
              }
            }}
            onSelect={(e) => {
              setCaret(e.currentTarget.selectionStart);
              session.moved(e.currentTarget.selectionStart);
            }}
            onPaste={(e) => {
              const files = [...e.clipboardData.files];
              if (files.length > 0) {
                e.preventDefault();
                void session.offer(files);
              }
            }}
            onContextMenu={(e) => {
              // A marked word opens the correction walk at it; anything else keeps the menu.
              const hit = [...(under.current?.querySelectorAll("mark") ?? [])].find((m) =>
                [...m.getClientRects()].some(
                  (r) => e.clientX >= r.left && e.clientX <= r.right && e.clientY >= r.top && e.clientY <= r.bottom,
                ),
              );
              const i = Number(hit?.dataset.mark ?? "-1");
              if (i >= 0) {
                e.preventDefault();
                session.openWalk(i);
              }
            }}
            onKeyDown={(e) => {
              if (walk !== undefined) {
                // The walk takes the keys while it is open, as the TUI's does.
                walkKey(e, session, took);
                return;
              }
              const choice = choices[pick];
              const into = caretInRTL(text, at) ? "ArrowLeft" : "ArrowRight";
              if (e.altKey && e.key === "s") {
                e.preventDefault();
                session.openWalk();
              } else if (e.altKey && /^[1-5]$/.test(e.key)) {
                if (took(session.chooseCompletion(Number(e.key) - 1))) {
                  e.preventDefault();
                }
              } else if (choice !== undefined && (e.key === "ArrowDown" || e.key === "ArrowUp")) {
                e.preventDefault();
                setChosen((pick + (e.key === "ArrowDown" ? 1 : choices.length - 1)) % choices.length);
              } else if (choice !== undefined && (e.key === "Tab" || (e.key === "Enter" && !typedWhole))) {
                e.preventDefault();
                moved(choice.take());
              } else if (offer !== undefined && e.key === "Tab" && !e.shiftKey) {
                if (took(session.complete(true))) {
                  e.preventDefault();
                } else if (offer.words.length > 0) {
                  // Nothing clearly leads: list the words instead.
                  e.preventDefault();
                  setListing(true);
                  setChosen(0);
                }
              } else if (offer !== undefined && e.key === into && !e.shiftKey && took(session.complete(false))) {
                e.preventDefault();
              } else if (e.key === "Enter" && !e.shiftKey) {
                e.preventDefault();
                void session.send();
              } else if (e.key === "Escape") {
                if (listing) {
                  setListing(false);
                } else {
                  session.cancel();
                }
              }
            }}
          />
        </div>
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
          {c.draft.editing === "" && (
            <button
              type="button"
              aria-expanded={later}
              title="Send the words later: at a time, or after a delay"
              onClick={() => {
                setLater(!later);
              }}
            >
              Later
            </button>
          )}
          {c.assist.marker && (
            <span className="detail model-marker" title="The completion model on this machine">
              ⌂ {c.assist.marker}
            </span>
          )}
          <button type="button" className="send" onClick={() => void session.send()}>
            Send
          </button>
        </div>
      </div>
    </section>
  );
}
