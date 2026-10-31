// The composer's spelling, drawn: the marks and the ghost text under the textarea (a
// copy of its words, laid out alike, the words themselves transparent), and the
// correction walk over the marks with its suggestions.
import type { KeyboardEvent, Ref } from "react";

import type { Session } from "./session";
import { type Mark, pieces, type SpellSettings, type Walk as WalkState, walkAction, walkRows } from "./spell";

/** markClass is how a mark is underlined: [spell] underline, or rare_underline for a rare word. */
function markClass(m: Mark, s: SpellSettings): string {
  return `spell spell-${m.rare ? s.rareUnderline : s.underline}${m.rare ? " spell-rare" : ""}`;
}

/**
 * Underlay is the words under the textarea, each mark drawn (none at the caret, -1 for
 * none hidden) and the ghost after them. Each mark carries its place in marks, for the
 * walk a click on it opens.
 */
export function Underlay({
  ref,
  text,
  marks,
  settings,
  caret,
  ghost,
}: {
  ref: Ref<HTMLDivElement>;
  text: string;
  marks: readonly Mark[];
  settings: SpellSettings;
  caret: number;
  ghost: string;
}) {
  return (
    <div ref={ref} className="compose-under" dir="auto" aria-hidden="true">
      {pieces(text, marks, caret, settings).map((p, i) => {
        const m = p.mark === undefined ? undefined : marks[p.mark];
        return m === undefined ? (
          <span key={i}>{p.text}</span>
        ) : (
          <mark key={i} className={markClass(m, settings)} data-mark={p.mark}>
            {p.text}
          </mark>
        );
      })}
      {ghost !== "" && <span className="ghost">{ghost}</span>}
      {/* A last line break is drawn as the textarea draws it. */}
      {"​"}
    </div>
  );
}

/** Walk is the correction walk: the word, its suggestions, and what else may be done with it. */
export function Walk({
  walk,
  session,
  took,
}: {
  walk: WalkState;
  session: Session;
  took: (caret: number | undefined) => boolean;
}) {
  const m = walk.items[walk.at];
  if (m === undefined) {
    return null;
  }
  const button = (label: string, key: string, act: () => void) => (
    <button
      type="button"
      title={key}
      onMouseDown={(e) => {
        e.preventDefault(); // the textarea keeps the focus
        act();
      }}
    >
      {label}
    </button>
  );
  return (
    <div className="spell-walk" role="dialog" aria-label="Spelling">
      <p>
        <strong dir="auto">{m.word}</strong>{" "}
        <span className="detail">
          {walk.at + 1}/{walk.items.length}
        </span>
      </p>
      {m.rare && <p className="detail">rarely written — one of these?</p>}
      {m.suggestions.length === 0 ? (
        <p className="detail">nothing to suggest</p>
      ) : (
        <ol className="spell-suggestions">
          {m.suggestions.slice(0, walkRows).map((s, i) => (
            <li key={s}>
              {button(`${String(i + 1)}  ${s}`, String(i + 1), () => {
                took(session.walkTake(i));
              })}
            </li>
          ))}
        </ol>
      )}
      <div className="spell-walk-actions">
        {button(m.rare ? "It's fine" : "Add to dictionary", "a", () => {
          session.walkLearn(true);
        })}
        {button("Ignore", "i", () => {
          session.walkLearn(false);
        })}
        {button("Skip", "s", () => {
          session.walkSkip();
        })}
        {button("Ignore all", "I", () => {
          session.walkIgnoreAll();
        })}
        {button("Skip all", "S", () => void session.walkSkipAll())}
        {button("Stop", "esc", () => {
          session.walkStop();
        })}
      </div>
    </div>
  );
}

/** walkKey does what a key pressed in the composer does while the walk is open. */
export function walkKey(
  e: KeyboardEvent<HTMLTextAreaElement>,
  session: Session,
  took: (caret: number | undefined) => boolean,
): void {
  const action = walkAction(e.key, e.ctrlKey || e.metaKey || e.altKey);
  if (action === undefined) {
    return;
  }
  e.preventDefault();
  switch (action.kind) {
    case "take":
      took(session.walkTake(action.n));
      break;
    case "add":
      session.walkLearn(true);
      break;
    case "ignore":
      session.walkLearn(false);
      break;
    case "skip":
      session.walkSkip();
      break;
    case "ignoreAll":
      session.walkIgnoreAll();
      break;
    case "skipAll":
      void session.walkSkipAll();
      break;
    case "stop":
      session.walkStop();
      break;
    case "swallow":
      break;
  }
}
