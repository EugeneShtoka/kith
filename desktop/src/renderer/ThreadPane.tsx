// The thread open beside the room: its root and replies, older replies on asking. The
// room's composer writes into it while it is open.
import { useEffect } from "react";

import type { ComposerView } from "./compose";
import type { Session } from "./session";
import type { ThreadView } from "./threads";
import type { TimelineView } from "./timeline";
import { Row, useWatched } from "./TimelinePane";

export function ThreadPane({
  thread,
  t,
  composer,
  session,
}: {
  thread: ThreadView;
  t: TimelineView;
  composer: ComposerView | undefined;
  session: Session;
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
  const byId = new Map(thread.rows.map((m) => [m.id, m]));
  const root = thread.rows[0]?.id === thread.root ? thread.rows[0] : undefined;
  return (
    <aside className="thread" aria-label="Thread">
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
        {thread.rows.map((m) => (
          <Row
            key={m.id}
            m={m}
            target={false}
            newSince={false}
            quoted={m.replyTo === "" ? undefined : (byId.get(m.replyTo) ?? t.quotes.get(m.replyTo))}
            reactions={t.reactions.get(m.id)}
            threads={undefined}
            composer={composer}
            session={session}
          />
        ))}
      </div>
    </aside>
  );
}
