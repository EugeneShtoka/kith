// The open room's timeline, drawn: the main timeline's messages, each thread a row under
// the message it hangs from (its replies are the thread pane's), oldest at the top,
// scrolled back a page at a time.
import { timestampDate } from "@bufbuild/protobuf/wkt";
import { type Ref, useEffect, useImperativeHandle, useRef, useState } from "react";
import { Virtuoso, type VirtuosoHandle } from "react-virtuoso";

import type { Message, Reaction } from "../gen/backend/v1/types_pb";
import { Avatar } from "./Avatar";
import type { ComposerView } from "./compose";
import { EmojiPicker } from "./EmojiPicker";
import { hueOf } from "./faces";
import { type Faces, faceClass, senderColor } from "./theme";
import { builtIn, ColorRow } from "./ThemeRow";
import { continues, dayLabel, newDay, seek } from "./groups";
import { attachmentURL } from "./media";
import { PollBlock } from "./Poll";
import type { Session } from "./session";
import { bound, type Keys, moved } from "./keymap";
import { FormattedText } from "./spans";
import { collapse, type ThreadSummary } from "./threads";
import { tally, type TimelineView } from "./timeline";

/** Where the list's indices start: lowered by each page put on top, so scrollback holds still. */
const firstIndex = 1_000_000;

const clock = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" });
const hours = new Intl.DateTimeFormat(undefined, { timeStyle: "short" });

/** TimelineNav moves the timeline's message cursor, the focused message, and acts on it. */
export interface TimelineNav {
  /** move takes a [keys.nav] motion; false for a path that is none, or no messages. */
  move(path: string, count: number): boolean;
  /** act takes a [keys.timeline] action on the cursor's message; false where it does not apply. */
  act(path: string, count: number): boolean;
  /** focus focuses the cursor's message, or the newest on screen when there is none. */
  focus(): void;
}

export function TimelinePane({
  t,
  unreadLine,
  composer,
  keys,
  faces,
  session,
  ref,
}: {
  t: TimelineView;
  unreadLine: boolean;
  composer: ComposerView | undefined;
  keys: Keys;
  faces: Faces;
  session: Session;
  ref?: Ref<TimelineNav>;
}) {
  const { main: shown, threads } = collapse(t.rows);
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

  // A jump's message, once found, is scrolled to once; the window is then scrolled freely.
  const list = useRef<VirtuosoHandle>(null);
  const scrolled = useRef("");
  const targetAt = t.target === "" ? -1 : shown.findIndex((m) => m.id === t.target);
  useEffect(() => {
    const jump = `${t.room} ${String(t.jumps)}`;
    if (targetAt >= 0 && scrolled.current !== jump) {
      scrolled.current = jump;
      list.current?.scrollToIndex({ index: targetAt, align: "center" });
    }
  }, [t.room, t.jumps, targetAt]);

  // The cursor is the message focused last, in its room: kept across its row scrolling out of the list.
  const pane = useRef<HTMLElement>(null);
  const cursor = useRef({ room: t.room, id: "" });
  useImperativeHandle(ref, () => {
    const onScreen = () => {
      const view = pane.current?.getBoundingClientRect();
      const rows = [...(pane.current?.querySelectorAll<HTMLElement>("article.message[data-id]") ?? [])];
      return rows.filter((r) => {
        const box = r.getBoundingClientRect();
        return view !== undefined && box.bottom > view.top && box.top < view.bottom;
      });
    };
    /** select moves the cursor to at, then hands its row, drawn and focused, to then. */
    const select = (at: number, then?: (row: HTMLElement) => void) => {
      const id = shown[at]?.id ?? "";
      cursor.current = { room: t.room, id };
      list.current?.scrollIntoView({
        index: at,
        done: () => {
          const row = pane.current?.querySelector<HTMLElement>(`article[data-id="${CSS.escape(id)}"]`);
          row?.focus();
          if (row) {
            then?.(row);
          }
        },
      });
    };
    const write = () => document.querySelector<HTMLElement>(".composer textarea")?.focus();
    const at = () => {
      const i = cursor.current.room === t.room ? shown.findIndex((m) => m.id === cursor.current.id) : -1;
      const newest = onScreen().at(-1)?.dataset.id;
      return i >= 0 ? i : newest === undefined ? shown.length - 1 : shown.findIndex((m) => m.id === newest);
    };
    return {
      move(path, count) {
        const to = shown.length === 0 ? undefined : moved(path, count, at(), onScreen().length, shown.length);
        if (to === undefined) {
          return false;
        }
        // shortcut: the oldest pulls one page more in, where the TUI's home loads to the start; load on to t.start if that is missed
        if (to === 0 && !t.start && (path === "nav.select_oldest" || path === "nav.scroll_oldest")) {
          session.older();
        }
        select(to);
        return true;
      },
      act(path, count) {
        const i = at();
        const m = shown[i];
        if (m === undefined) {
          return false;
        }
        const sought = seek(shown, i, path, count);
        if (sought !== undefined) {
          select(sought);
          return true;
        }
        // The row's own buttons, where what they open is the row's: the palette, the question.
        const press = (row: HTMLElement) => row.querySelector<HTMLElement>(`[data-key="${path}"]`)?.click();
        switch (path) {
          case "timeline.insert":
            if (composer === undefined) {
              return false;
            }
            write();
            return true;
          case "timeline.reply":
            if (composer === undefined || m.redacted) {
              return false;
            }
            session.reply(m);
            write();
            return true;
          case "timeline.edit":
            if (composer?.selves.has(m.sender) !== true || m.redacted) {
              return false;
            }
            session.edit(m);
            write();
            return true;
          case "timeline.go_reply":
            if (m.replyTo === "") {
              return false;
            }
            cursor.current = { room: t.room, id: m.replyTo };
            session.jump(t.room, m.replyTo);
            return true;
          case "timeline.person":
            session.openPerson({ person: m.sender }, m.senderName || m.sender);
            return true;
          case "timeline.download":
            if (m.media === undefined) {
              return false;
            }
            void session.save(m);
            return true;
          case "timeline.start_thread":
            session.openThread(m.id);
            return true;
          case "timeline.react":
          case "timeline.redact":
          case "timeline.open_thread":
            select(i, press);
            return true;
          case "timeline.play":
            select(i, (row) => {
              const player = row.querySelector<HTMLMediaElement>("audio, video");
              if (player?.paused) {
                void player.play();
              } else {
                player?.pause();
              }
            });
            return m.media?.type === "audio" || m.media?.type === "video";
          case "timeline.vote":
            select(i, (row) => row.querySelector<HTMLElement>("button.poll-option, .poll-option input")?.focus());
            return m.poll !== undefined;
        }
        return false;
      },
      focus() {
        if (shown.length > 0) {
          select(at());
        } else {
          pane.current?.focus();
        }
      },
    };
  });

  const byId = new Map(t.rows.map((m) => [m.id, m]));
  return (
    <section
      className="timeline"
      aria-label="Messages"
      ref={pane}
      tabIndex={-1}
      onFocus={(e) => {
        const id = (e.target as Element).closest<HTMLElement>("article.message")?.dataset.id;
        if (id !== undefined) {
          cursor.current = { room: t.room, id };
        }
      }}
    >
      {t.loading && shown.length === 0 && <p className="detail">Loading…</p>}
      <Virtuoso
        ref={list}
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
              before={before}
              target={m.id === t.target}
              newSince={unreadLine && before !== undefined && before.id === t.unreadFrom}
              quoted={m.replyTo === "" ? undefined : (byId.get(m.replyTo) ?? t.quotes.get(m.replyTo))}
              reactions={t.reactions.get(m.id)}
              threads={threads.filter((th) => th.anchor === m.id || (th.anchor === "" && before === undefined))}
              composer={composer}
              keys={keys}
              faces={faces}
              session={session}
            />
          );
        }}
      />
    </section>
  );
}

/** useWatched: the window is focused and showing, so what it shows is being read. */
export function useWatched(): boolean {
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

/** Row is one message, in the room's timeline or a thread's pane (threads: none). */
export function Row({
  m,
  before,
  target,
  newSince,
  quoted,
  reactions,
  threads,
  composer,
  keys,
  faces,
  session,
}: {
  m: Message;
  /** The row drawn above it: a new day, or another sender, starts a group. */
  before: Message | undefined;
  /** The message a jump was to. */
  target: boolean;
  newSince: boolean;
  quoted: Message | undefined;
  reactions: readonly Reaction[] | undefined;
  /** The threads drawn under it; undefined in a thread's pane, where none starts. */
  threads: readonly ThreadSummary[] | undefined;
  composer: ComposerView | undefined;
  keys: Keys;
  /** Whose colors people are drawn in. */
  faces: Faces;
  session: Session;
}) {
  const at = m.timestamp ? timestampDate(m.timestamp) : undefined;
  const grouped = continues(before, m, newSince);
  const classes = ["message"];
  if (target) {
    classes.push("target");
  }
  if (grouped) {
    classes.push("grouped");
  }
  return (
    <>
      {at !== undefined && newDay(before, m) && (
        <div className="day" role="separator">
          <span>{dayLabel(at, new Date())}</span>
        </div>
      )}
      {newSince && (
        <div className="unread-line" role="separator">
          New
        </div>
      )}
      <article className={classes.join(" ")} data-id={m.id} tabIndex={-1}>
        <div className="gutter">
          {grouped ? (
            at && (
              <time className="detail" title={clock.format(at)}>
                {hours.format(at)}
              </time>
            )
          ) : (
            <Avatar id={m.sender} name={m.senderName || m.sender} hue={faceClass(faces, m.sender)} />
          )}
        </div>
        <div className="content">
          {!grouped && (
            <header>
              <button
                type="button"
                className={`sender sender-card ${faceClass(faces, m.sender)}`}
                dir="auto"
                title="Their phonebook card"
                onClick={() => {
                  session.openPerson({ person: m.sender }, m.senderName || m.sender);
                }}
              >
                {m.senderName || m.sender}
              </button>{" "}
              {at && (
                <time className="detail" title={clock.format(at)}>
                  {hours.format(at)}
                </time>
              )}
              {m.edited && !m.redacted && <span className="detail"> (edited)</span>}
            </header>
          )}
          {m.replyTo !== "" && <Quote quoted={quoted} faces={faces} />}
          <Body m={m} composer={composer} session={session} />
          {grouped && m.edited && !m.redacted && <span className="detail edited">(edited)</span>}
          {reactions !== undefined && reactions.length > 0 && (
            <ul className="reactions" aria-label="Reactions">
              {tally(reactions).map((r) => (
                <li key={r.key}>
                  {/* Pressed: added, or taken back where it is yours. */}
                  <button
                    type="button"
                    aria-pressed={reactions.some((x) => x.key === r.key && composer?.selves.has(x.sender) === true)}
                    onClick={() => void session.react(m, r.key)}
                  >
                    {r.key} {r.count}
                  </button>
                </li>
              ))}
            </ul>
          )}
          {threads?.map((th) => (
            <button
              key={th.root}
              type="button"
              className="thread-row"
              data-key="timeline.open_thread"
              onClick={() => {
                session.openThread(th.root);
              }}
            >
              {th.count === 1 ? "1 reply" : `${String(th.count)} replies`}
              {th.rootLoaded ? "" : " in a thread"} · <span dir="auto">{th.latestSenderName || th.latestSender}</span>
            </button>
          ))}
          {composer !== undefined && !m.redacted && (
            <Actions
              m={m}
              c={composer}
              keys={keys}
              startsThread={threads !== undefined}
              faces={faces}
              session={session}
            />
          )}
        </div>
      </article>
    </>
  );
}

/** Actions are what the person can do with a message: answer it, react, correct or delete their own. */
function Actions({
  m,
  c,
  keys,
  startsThread,
  faces,
  session,
}: {
  m: Message;
  c: ComposerView;
  keys: Keys;
  startsThread: boolean;
  faces: Faces;
  session: Session;
}) {
  const [open, setOpen] = useState<"palette" | "picker" | "delete" | "color" | undefined>(undefined);
  const bar = useRef<HTMLDivElement>(null);
  /** shut closes the reactions offered, the cursor back on the message. */
  const shut = () => {
    setOpen(undefined);
    bar.current?.closest<HTMLElement>("article.message")?.focus();
  };
  const mine = c.selves.has(m.sender);
  const react = (key: string) => {
    setOpen(undefined);
    void session.react(m, key);
  };
  return (
    <>
      {/* The hover toolbar: kept up while one of its panels is open. */}
      <div
        ref={bar}
        className={open === undefined ? "actions" : "actions open"}
        role="toolbar"
        aria-label="Message actions"
      >
        <button
          type="button"
          data-key="timeline.react"
          aria-label="React"
          title="React"
          aria-expanded={open === "palette"}
          onClick={() => {
            setOpen(open === "palette" ? undefined : "palette");
          }}
        >
          ☺
        </button>
        <button
          type="button"
          aria-label="Reply"
          title="Reply"
          onClick={() => {
            session.reply(m);
          }}
        >
          ↩
        </button>
        {startsThread && (
          <button
            type="button"
            aria-label="Thread"
            title="Reply in a thread"
            onClick={() => {
              session.openThread(m.id);
            }}
          >
            ⤷
          </button>
        )}
        {faces.theme !== undefined && (
          <button
            type="button"
            aria-label="Color"
            title="Their color"
            aria-expanded={open === "color"}
            onClick={() => {
              setOpen(open === "color" ? undefined : "color");
            }}
          >
            ◐
          </button>
        )}
        {mine && (
          <button
            type="button"
            aria-label="Edit"
            title="Edit"
            onClick={() => {
              session.edit(m);
            }}
          >
            ✎
          </button>
        )}
        {mine && (
          <button
            type="button"
            data-key="timeline.redact"
            aria-label="Delete"
            title="Delete"
            onClick={() => {
              setOpen("delete");
            }}
          >
            ✕
          </button>
        )}
      </div>
      {open === "palette" && (
        <span
          className="palette"
          role="group"
          aria-label="Quick reactions"
          onKeyDown={(e) => {
            if (bound(keys, "react.cancel", e)) {
              e.preventDefault();
              shut();
            }
          }}
        >
          {c.emoji.palette.map((e, i) => (
            <button
              key={e}
              type="button"
              autoFocus={i === 0}
              onClick={() => {
                react(e);
              }}
            >
              {e}
            </button>
          ))}
          <button
            type="button"
            onClick={() => {
              setOpen("picker");
            }}
          >
            More
          </button>
        </span>
      )}
      {open === "picker" && <EmojiPicker emoji={c.emoji} keys={keys} kind="reaction" onPick={react} onClose={shut} />}
      {open === "color" && faces.theme !== undefined && (
        <span className="palette" role="group" aria-label="Their color">
          <ColorRow
            label={m.senderName || m.sender}
            k={`people.${m.sender}`}
            theme={faces.theme}
            fallback={
              senderColor(faces.theme, faces.slots, m.sender) ??
              builtIn(`--hue-${String(hueOf(m.sender))}`, window.matchMedia("(prefers-color-scheme: dark)").matches)
            }
            session={session}
          />
        </span>
      )}
      {open === "delete" && (
        <span className="banner waiting">
          Delete this message for everyone?{" "}
          <button
            type="button"
            onClick={() => {
              setOpen(undefined);
              void session.remove(m);
            }}
          >
            Delete
          </button>{" "}
          <button
            type="button"
            autoFocus
            onClick={() => {
              setOpen(undefined);
            }}
          >
            Keep
          </button>
        </span>
      )}
    </>
  );
}

function Quote({ quoted, faces }: { quoted: Message | undefined; faces: Faces }) {
  if (quoted === undefined) {
    return <blockquote className="reply detail">In reply to a message not loaded</blockquote>;
  }
  const words = quoted.redacted ? "Message deleted" : (quoted.format?.text ?? quoted.body);
  return (
    <blockquote className="reply" dir="auto">
      <span className={`sender ${faceClass(faces, quoted.sender)}`}>{quoted.senderName || quoted.sender}</span>{" "}
      <span className={quoted.redacted ? "deleted" : undefined}>{words.split("\n")[0]}</span>
    </blockquote>
  );
}

function Body({ m, composer, session }: { m: Message; composer: ComposerView | undefined; session: Session }) {
  if (m.redacted) {
    return (
      <p className="body deleted">
        Message deleted
        {m.redactedBy && m.redactedBy !== m.sender && <> by {m.redactedBy}</>}
        {m.redactedReason && <>: {m.redactedReason}</>}
      </p>
    );
  }
  if (m.poll !== undefined) {
    return <PollBlock m={m} p={m.poll} composer={composer} session={session} />;
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
      {media?.type === "audio" && (
        <audio className="player" controls preload="metadata" src={attachmentURL(m)} aria-label={media.name} />
      )}
      {media?.type === "video" && (
        <video
          className="player picture"
          controls
          preload="metadata"
          src={attachmentURL(m)}
          aria-label={media.name}
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
