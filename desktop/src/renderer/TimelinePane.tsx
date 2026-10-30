// The open room's timeline, drawn: the main timeline's messages (a thread's replies are
// the thread pane's), oldest at the top, scrolled back a page at a time.
import { timestampDate } from "@bufbuild/protobuf/wkt";
import { useEffect, useState } from "react";
import { Virtuoso } from "react-virtuoso";

import type { Message, Reaction } from "../gen/backend/v1/types_pb";
import { attachmentURL } from "./media";
import type { Session } from "./session";
import { FormattedText } from "./spans";
import { tally, type TimelineView } from "./timeline";

/** Where the list's indices start: lowered by each page put on top, so scrollback holds still. */
const firstIndex = 1_000_000;

const clock = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" });

export function TimelinePane({ t, unreadLine, session }: { t: TimelineView; unreadLine: boolean; session: Session }) {
  const shown = t.rows.filter((m) => m.threadRoot === "");
  // The index of the first row: lowered by as many rows as a page put above it.
  const [anchor, setAnchor] = useState({ room: t.room, id: shown[0]?.id ?? "", index: firstIndex });
  const top = shown[0]?.id ?? "";
  if (anchor.room !== t.room || anchor.id !== top) {
    const was = anchor.room === t.room ? shown.findIndex((m) => m.id === anchor.id) : -1;
    setAnchor({ room: t.room, id: top, index: was > 0 ? anchor.index - was : anchor.index });
  }
  const watched = useWatched();
  const [atBottom, setAtBottom] = useState(true);
  useEffect(() => {
    session.watch(watched && atBottom);
  }, [session, watched, atBottom]);
  useEffect(
    () => () => {
      session.watch(false);
    },
    [session],
  );

  const byId = new Map(t.rows.map((m) => [m.id, m]));
  return (
    <section className="timeline" aria-label="Messages">
      {t.loading && shown.length === 0 && <p className="detail">Loading…</p>}
      <Virtuoso
        key={t.room}
        data={shown}
        firstItemIndex={anchor.index}
        initialTopMostItemIndex={Math.max(0, shown.length - 1)}
        alignToBottom
        followOutput="auto"
        computeItemKey={(_, m) => m.id}
        startReached={() => {
          if (!t.start) {
            session.older();
          }
        }}
        atBottomStateChange={setAtBottom}
        itemContent={(index, m) => {
          const before = shown[index - anchor.index - 1];
          return (
            <Row
              m={m}
              newSince={unreadLine && before !== undefined && before.id === t.unreadFrom}
              quoted={m.replyTo === "" ? undefined : (byId.get(m.replyTo) ?? t.quotes.get(m.replyTo))}
              reactions={t.reactions.get(m.id)}
              session={session}
            />
          );
        }}
      />
    </section>
  );
}

/** useWatched: the window is focused and showing, so what it shows is being read. */
function useWatched(): boolean {
  const now = () => document.hasFocus() && document.visibilityState === "visible";
  const [watched, setWatched] = useState(now);
  useEffect(() => {
    const update = () => {
      setWatched(now());
    };
    window.addEventListener("focus", update);
    window.addEventListener("blur", update);
    document.addEventListener("visibilitychange", update);
    return () => {
      window.removeEventListener("focus", update);
      window.removeEventListener("blur", update);
      document.removeEventListener("visibilitychange", update);
    };
  }, []);
  return watched;
}

function Row({
  m,
  newSince,
  quoted,
  reactions,
  session,
}: {
  m: Message;
  newSince: boolean;
  quoted: Message | undefined;
  reactions: readonly Reaction[] | undefined;
  session: Session;
}) {
  return (
    <article className="message">
      {newSince && (
        <div className="unread-line" role="separator">
          New
        </div>
      )}
      <header>
        <span className="sender" dir="auto">
          {m.senderName || m.sender}
        </span>{" "}
        {m.timestamp && <time className="detail">{clock.format(timestampDate(m.timestamp))}</time>}
        {m.edited && !m.redacted && <span className="detail"> (edited)</span>}
      </header>
      {m.replyTo !== "" && <Quote quoted={quoted} />}
      <Body m={m} session={session} />
      {reactions !== undefined && reactions.length > 0 && (
        <ul className="reactions" aria-label="Reactions">
          {tally(reactions).map((r) => (
            <li key={r.key}>
              {r.key} {r.count}
            </li>
          ))}
        </ul>
      )}
    </article>
  );
}

function Quote({ quoted }: { quoted: Message | undefined }) {
  if (quoted === undefined) {
    return <blockquote className="reply detail">In reply to a message not loaded</blockquote>;
  }
  const words = quoted.redacted ? "Message deleted" : (quoted.format?.text ?? quoted.body);
  return (
    <blockquote className="reply" dir="auto">
      <span className="sender">{quoted.senderName || quoted.sender}</span>{" "}
      <span className={quoted.redacted ? "deleted" : undefined}>{words.split("\n")[0]}</span>
    </blockquote>
  );
}

function Body({ m, session }: { m: Message; session: Session }) {
  if (m.redacted) {
    return (
      <p className="body deleted">
        Message deleted
        {m.redactedBy && m.redactedBy !== m.sender && <> by {m.redactedBy}</>}
        {m.redactedReason && <>: {m.redactedReason}</>}
      </p>
    );
  }
  const media = m.media;
  const caption = media === undefined || m.body !== media.name;
  return (
    <>
      {media?.type === "image" && (
        <img
          className="picture"
          src={attachmentURL(m)}
          alt={media.name}
          width={media.width > 0n ? Number(media.width) : undefined}
          height={media.height > 0n ? Number(media.height) : undefined}
        />
      )}
      {media !== undefined && (
        <button
          type="button"
          className="attachment"
          onClick={() => {
            void session.save(m);
          }}
        >
          Save {media.name || "attachment"}
          {media.size > 0n && <span className="detail"> ({sizeText(media.size)})</span>}
        </button>
      )}
      {caption && (
        <p className={m.emote ? "body emote" : "body"} dir="auto">
          {m.format ? <FormattedText f={m.format} /> : m.body}
        </p>
      )}
    </>
  );
}

function sizeText(bytes: bigint): string {
  const n = Number(bytes);
  if (n < 1024) {
    return `${String(n)} B`;
  }
  if (n < 1024 * 1024) {
    return `${(n / 1024).toFixed(0)} KB`;
  }
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}
