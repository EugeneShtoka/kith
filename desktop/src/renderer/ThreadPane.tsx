// The thread open beside the room: its root and replies, older replies on asking. The
// room's composer writes into it while it is open. Its cursor is the timeline's: the
// focused message, moved by [keys.nav] and acted on by [keys.timeline].
import { type Ref, useEffect, useImperativeHandle, useRef } from "react";

import type { ComposerView } from "./compose";
import { type Keys, moved } from "./keymap";
import type { Faces } from "./theme";
import type { Session } from "./session";
import type { ThreadView } from "./threads";
import type { TimelineView } from "./timeline";
import { actOn, Row, type TimelineNav, useWatched } from "./TimelinePane";

export function ThreadPane({
  thread,
  t,
  composer,
  keys,
  faces,
  session,
  ref,
}: {
  thread: ThreadView;
  t: TimelineView;
  composer: ComposerView | undefined;
  keys: Keys;
  faces: Faces;
  session: Session;
  ref?: Ref<TimelineNav>;
}) {
  const watched = useWatched();
  useEffect(() => {
    session.watchThread(watched);
  }, [session, watched]);
  useEffect(
    () => () => {
      session.watchThread(false);
    },
    [session],
  );
  const pane = useRef<HTMLElement>(null);
  const cursor = useRef({ root: thread.root, id: "" });
  useImperativeHandle(ref, () => {
    const rows = thread.rows;
    const drawn = () => [...(pane.current?.querySelectorAll<HTMLElement>("article.message[data-id]") ?? [])];
    const select = (at: number, then?: (row: HTMLElement) => void) => {
      const id = rows[at]?.id ?? "";
      cursor.current = { root: thread.root, id };
      const row = drawn().find((r) => r.dataset.id === id);
      row?.focus();
      if (row) {
        then?.(row);
      }
    };
    // The cursor's message, else the newest reply.
    const at = () => {
      const i = cursor.current.root === thread.root ? rows.findIndex((m) => m.id === cursor.current.id) : -1;
      return i >= 0 ? i : rows.length - 1;
    };
    return {
      move(path, count) {
        const view = pane.current?.getBoundingClientRect();
        const page = drawn().filter((r) => {
          const box = r.getBoundingClientRect();
          return view !== undefined && box.top >= view.top && box.bottom <= view.bottom;
        }).length;
        const to = rows.length === 0 ? undefined : moved(path, count, at(), page, rows.length);
        if (to === undefined) {
          return false;
        }
        select(to);
        return true;
      },
      act(path, count) {
        return actOn(path, count, rows, at(), select, {
          room: t.room,
          composer,
          session,
          inThread: true,
          aim: (id) => {
            cursor.current = { root: thread.root, id };
          },
        });
      },
      focus() {
        if (rows.length > 0) {
          select(at());
        } else {
          pane.current?.focus();
        }
      },
    };
  });
  const byId = new Map(thread.rows.map((m) => [m.id, m]));
  const root = thread.rows[0]?.id === thread.root ? thread.rows[0] : undefined;
  return (
    <aside
      className="thread"
      aria-label="Thread"
      data-pane="thread"
      ref={pane}
      tabIndex={-1}
      onFocus={(e) => {
        const id = (e.target as Element).closest<HTMLElement>("article.message")?.dataset.id;
        if (id !== undefined) {
          cursor.current = { root: thread.root, id };
        }
      }}
    >
      <header>
        <span className="detail">Thread</span>
        <button
          type="button"
          onClick={() => {
            session.closeThread();
          }}
        >
          Close
        </button>
      </header>
      {root === undefined && <p className="detail">The message that started it is not cached</p>}
      {!thread.start && (
        <button
          type="button"
          disabled={thread.loading}
          onClick={() => {
            session.olderInThread();
          }}
        >
          {thread.loading ? "Loading…" : "Older replies"}
        </button>
      )}
      <div className="thread-rows">
        {thread.rows.map((m, i) => (
          <Row
            key={m.id}
            m={m}
            before={thread.rows[i - 1]}
            target={false}
            newSince={false}
            quoted={m.replyTo === "" ? undefined : (byId.get(m.replyTo) ?? t.quotes.get(m.replyTo))}
            reactions={t.reactions.get(m.id)}
            threads={undefined}
            composer={composer}
            keys={keys}
            faces={faces}
            session={session}
          />
        ))}
      </div>
    </aside>
  );
}
