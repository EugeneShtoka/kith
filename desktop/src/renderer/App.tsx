import { timestampDate } from "@bufbuild/protobuf/wkt";
import { type ReactNode, type RefObject, useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";

import { NetworkPhase, type NetworkStatus, type SeatHolder, type StatusResponse } from "../gen/backend/v1/services_pb";
import type { Room, ThreadUnread, Unread } from "../gen/backend/v1/types_pb";
import type { ThemeResponse } from "../gen/device/v1/device_pb";
import { Avatar } from "./Avatar";
import { ComposerPane } from "./ComposerPane";
import { networkMark } from "./faces";
import { FilingDialog } from "./Filing";
import { LinkingDialog } from "./Linking";
import { MarkReadDialog } from "./MarkRead";
import { RoomNameDialog, ShortcutDialog } from "./RoomName";
import { helpSections } from "./help";
import { HelpOverlay } from "./HelpOverlay";
import { roomPicture } from "./media";
import { type Action, keyName, Keymap, moved, typing } from "./keymap";
import { OfferDialog } from "./Offer";
import { PeopleDialog, PersonDialog } from "./People";
import { colorClass, drawsPictures, themeCSS, modeOf, roomColor } from "./theme";
import { namesRow, rowUnread } from "./rail";
import { unreadOf } from "./rooms";
import { type Link, peekAfter, type Seat, Session, type View } from "./session";
import { SearchBar, SearchResults, Switcher } from "./Find";
import { SettingsPage } from "./SettingsPage";
import { ThreadPane } from "./ThreadPane";
import { type TimelineNav, TimelinePane, write } from "./TimelinePane";
import { VerifyDialog } from "./Verification";

const phases: Record<NetworkPhase, string> = {
  [NetworkPhase.UNSPECIFIED]: "unknown",
  [NetworkPhase.LOGGED_OUT]: "logged out",
  [NetworkPhase.CONNECTING]: "connecting",
  [NetworkPhase.ONLINE]: "online",
  [NetworkPhase.FAILED]: "failed",
};

const when = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "medium" });
const clock = new Intl.DateTimeFormat(undefined, { timeStyle: "short" });

export function App() {
  const [session] = useState(() => new Session());
  useEffect(() => {
    session.start();
    return () => {
      session.stop();
    };
  }, [session]);
  const view = useSyncExternalStore(session.subscribe, session.snapshot);
  useEffect(() => {
    document.title = view.unreadTotal > 0 ? `kith (${String(view.unreadTotal)})` : "kith";
  }, [view.unreadTotal]);
  useEffect(() => {
    paint(view.theme);
  }, [view.theme]);

  const seated = view.profile !== "" && view.seat.state === "seated" && view.settingsPage === undefined;
  return (
    <main className="app">
      <VerifyDialog verify={view.verify} session={session} />
      {view.offers.prompt && (
        <OfferDialog key={view.offers.prompt.kind} prompt={view.offers.prompt} session={session} />
      )}
      {seated && view.filing && <FilingDialog filing={view.filing} keys={view.keys} session={session} />}
      {seated && view.naming && <RoomNameDialog naming={view.naming} keys={view.keys} session={session} />}
      {seated && view.shortcut && <ShortcutDialog shortcut={view.shortcut} keys={view.keys} session={session} />}
      {seated && view.marking && <MarkReadDialog marking={view.marking} keys={view.keys} session={session} />}
      {seated && view.linking && <LinkingDialog linking={view.linking} keys={view.keys} session={session} />}
      {seated && view.people.list && (
        <PeopleDialog list={view.people.list} covered={view.people.card !== undefined} session={session} />
      )}
      {seated && view.people.card && <PersonDialog card={view.people.card} session={session} />}
      {seated ? (
        <Seated view={view} session={session} />
      ) : (
        <>
          <TopBar view={view} session={session}>
            <Profile view={view} session={session} />
          </TopBar>
          {view.problem && <p className="problem">{view.problem}</p>}
          <div className="page">
            {view.settingsPage ? (
              <SettingsPage page={view.settingsPage} view={view} session={session} />
            ) : (
              <Body view={view} session={session} />
            )}
            <Where view={view} />
          </div>
        </>
      )}
    </main>
  );
}

/** seatedActions are the [keys] actions the seated window binds, in priority order. */
const seatedActions: readonly Action[] = [
  // The player's ahead of the panes', as the TUI asks them: they win while something plays.
  ...["play_pause", "back", "forward", "slower", "faster", "normal_speed", "stop"].map((action) => ({
    path: `player.${action}`,
    anywhere: false,
  })),
  { path: "jump_to", anywhere: true },
  { path: "jump_back", anywhere: true },
  { path: "jump_forward", anywhere: true },
  { path: "settings", anywhere: false },
  { path: "help", anywhere: false },
  { path: "focus_next", anywhere: true },
  { path: "focus_prev", anywhere: true },
  // The timeline's before nav's, as the TUI asks them: its enter goes to what a reply answers.
  ...[
    "insert",
    "reply",
    "go_reply",
    "react",
    "redact",
    "edit",
    "next_mention",
    "prev_mention",
    "next_attachment",
    "prev_attachment",
    "prev_day",
    "next_day",
    "person",
    "download",
    "play",
    "vote",
    "open_thread",
    "start_thread",
    "name",
    "copy_text",
    "copy_url",
    "open_url",
    "open_url_focus",
  ].map((action) => ({ path: `timeline.${action}`, anywhere: false })),
  { path: "rooms.spaces", anywhere: false },
  { path: "rooms.name", anywhere: false },
  { path: "rooms.person", anywhere: false },
  { path: "rooms.mark_read", anywhere: false },
  { path: "rail.mark_read", anywhere: false },
  ...[
    "up",
    "down",
    "open",
    "back",
    "page_up",
    "page_down",
    "half_page_up",
    "half_page_down",
    "select_oldest",
    "select_newest",
    "scroll_oldest",
    "scroll_newest",
  ].map((motion) => ({ path: `nav.${motion}`, anywhere: false })),
];

/** The speeds the player keys keep to, and their step (audio.MinSpeed, MaxSpeed; the TUI's speedStep). */
const minSpeed = 0.25;
const maxSpeed = 4;
const speedStep = 0.25;

/** panes are the seated window's panes in focus order, each a [data-pane] element. */
const panes = ["rail", "rooms", "timeline", "thread"];

/** paneRows are the rows a list pane's cursor moves over: each focusable itself. */
const paneRows: Readonly<Record<string, string>> = {
  rail: "li.place > button",
  rooms: "li.room > button, li.thread > button",
};

/** paneOf is the pane el is in, "" for none; the body (nothing focused) is the pane focused last. */
function paneOf(el: Element | null, last: string): string {
  if (el === null || el === document.body) {
    return last;
  }
  return el.closest("[data-pane]")?.getAttribute("data-pane") ?? "";
}

/**
 * onRow: el is where a pane's motions act: the pane itself, one of its rows, its list's
 * scroller (which a click on the timeline between messages focuses), or nothing focused.
 */
function onRow(el: Element | null, pane: string): boolean {
  return (
    el === null ||
    el === document.body ||
    el.matches("[data-pane], [data-virtuoso-scroller]") ||
    el.matches(paneRows[pane] ?? "article.message")
  );
}

function rowsOf(pane: string): HTMLElement[] {
  return [...document.querySelectorAll<HTMLElement>(`[data-pane="${pane}"] ${paneRows[pane] ?? ""}`)];
}

/** focusPane focuses a list pane's current row (its first, if none is current): false where it is not shown. */
function focusPane(pane: string): boolean {
  const rows = rowsOf(pane);
  const el = rows.find((r) => r.getAttribute("aria-current") === "true") ?? rows[0];
  el?.focus();
  return el !== undefined && document.activeElement === el;
}

/** moveRow moves a list pane's cursor, the focused row; the rail's also picks the place, as the TUI's does. */
function moveRow(pane: string, path: string, count: number): boolean {
  const rows = rowsOf(pane);
  const focused = rows.indexOf(document.activeElement as HTMLElement);
  const at = Math.max(0, focused >= 0 ? focused : rows.findIndex((r) => r.getAttribute("aria-current") === "true"));
  const box = document.querySelector(`[data-pane="${pane}"]`)?.getBoundingClientRect();
  const page = rows.filter((r) => {
    const b = r.getBoundingClientRect();
    return box !== undefined && b.top >= box.top && b.bottom <= box.bottom;
  }).length;
  const to = rows.length === 0 ? undefined : moved(path, count, at, page, rows.length);
  if (to === undefined) {
    return false;
  }
  rows[to]?.focus();
  if (pane === "rail" && to !== focused) {
    rows[to]?.click();
  }
  return true;
}

// The list's cursor shows its room once it stops a moment there (peekAfter).
let peeking: ReturnType<typeof setTimeout> | undefined;

/** narrow: the window is narrow enough that the rail folds by itself (styles.css). */
function narrow(): boolean {
  return window.matchMedia("(max-width: 960px)").matches;
}

/** TopBar runs across the window's top: what the screen puts there, and Settings. */
function TopBar({ view, session, children }: { view: View; session: Session; children?: ReactNode }) {
  return (
    <header className="topbar">
      {children}
      <button
        type="button"
        className="settings-button"
        aria-pressed={view.settingsPage !== undefined}
        onClick={() => {
          if (view.settingsPage) {
            session.closeSettings();
          } else {
            session.openSettings();
          }
        }}
      >
        Settings
      </button>
    </header>
  );
}

/** Profile is the profile menu, where the config has more than one. */
function Profile({ view, session }: { view: View; session: Session }) {
  if (view.profiles.length <= 1 || view.profile === "") {
    return null;
  }
  return (
    <select
      aria-label="Profile"
      className="profile"
      value={view.profile}
      onChange={(e) => {
        void session.choose(e.target.value);
      }}
    >
      {view.profiles.map((p) => (
        <option key={p} value={p}>
          {p}
        </option>
      ))}
    </select>
  );
}

/** Where says which kithd the window reaches, and how. */
function Where({ view }: { view: View }) {
  if (!view.socket && !view.bridge) {
    return null;
  }
  return (
    <footer>
      {view.bridge ? (
        <>
          kithd on bridge <code>{view.bridge}</code>
        </>
      ) : (
        <>
          kithd at <code>{view.socket}</code>
        </>
      )}
      , through kith-relay
    </footer>
  );
}

/** Body is the window before it is seated: which profile, and its seat. */
function Body({ view, session }: { view: View; session: Session }) {
  if (view.profile === "") {
    return view.profiles.length > 0 ? <Picker profiles={view.profiles} session={session} /> : null;
  }
  return <SeatState seat={view.seat} profile={view.profile} session={session} />;
}

/**
 * Seated is the window in its profile's seat: the search bar across the top, then the
 * columns, each scrolling on its own: the left one (profile, rail, rooms), the open room
 * with its composer docked under it, and a thread beside it. A toggle folds the rail
 * away, then the whole left column, then brings both back; in a narrow window the rail
 * is folded, and the toggle lays it (or under 640px the whole left column) over the room
 * until a room or a place is picked.
 */
function Seated({ view, session }: { view: View; session: Session }) {
  const [switching, setSwitching] = useState(false);
  const [helping, setHelping] = useState(false);
  // The left column unfolded over a narrow window: until a room or a place is picked.
  const here = `${view.open}\u0000${String(view.row)}`;
  const [unfoldedAt, setUnfoldedAt] = useState<string | undefined>(undefined);
  const unfolded = unfoldedAt === here;
  // What a wide window folds away: nothing, the rail, or the whole left column; the toggle steps through them.
  const [folded, setFolded] = useState<"none" | "places" | "rooms">("none");
  // [[keys.jump]] bound as jump.<i> and [[keys.tag]] as tag.<i>, after the window's own actions.
  const keys = useMemo(
    () => ({
      ...view.keys,
      ...Object.fromEntries(view.jumps.map((j, i) => [`jump.${String(i)}`, [j.sequence]])),
      ...Object.fromEntries(view.tagKeys.map((t, i) => [`tag.${String(i)}`, [t.sequence]])),
    }),
    [view.keys, view.jumps, view.tagKeys],
  );
  const keymap = useMemo(
    () =>
      new Keymap(keys, [
        ...seatedActions,
        ...view.jumps.map((_, i) => ({ path: `jump.${String(i)}`, anywhere: false })),
        ...view.tagKeys.map((_, i) => ({ path: `tag.${String(i)}`, anywhere: false })),
      ]),
    [keys, view.jumps, view.tagKeys],
  );
  const [pending, setPending] = useState("");
  const timeline = useRef<TimelineNav>(null);
  const thread = useRef<TimelineNav>(null);
  // The voice note or video played last: the player keys' while it is in the window and has not ended or stopped.
  // shortcut: a note that ends lets go of the keys at once, where the TUI's bar keeps them close_after; read that once the window has a bar
  const playing = useRef<HTMLMediaElement>(undefined);
  // A thread opened by a key takes the focus once its rows are drawn.
  const toThread = useRef(false);
  // A room opened from the list takes the focus once it is drawn: to its composer, else
  // (a room with none) to its timeline.
  const toTimeline = useRef(false);
  // enterRoom goes into a room from the list, the one its cursor shows or another.
  const enterRoom = (room: string) => {
    const shown = room === view.open;
    session.open(room); // a room the cursor showed is gone to now: back returns to it
    if (!shown) {
      toTimeline.current = true;
    } else if (!write()) {
      timeline.current?.focus();
    }
  };
  // enterThread goes into a thread from its row in the list: its room shown, the thread
  // open beside it and focused.
  const enterThread = (room: string, root: string) => {
    session.open(room);
    session.openThread(root);
    toThread.current = true;
    if (view.thread?.root === root) {
      toThread.current = false;
      thread.current?.focus();
    }
  };
  // The pane focused last, which keys go to while nothing is focused: the rooms before any.
  const lastPane = useRef("rooms");
  useEffect(() => {
    if (toTimeline.current && view.timeline) {
      toTimeline.current = false;
      if (!write()) {
        timeline.current?.focus();
      }
    }
  }, [view.open, view.timeline]);
  const threadRows = view.thread?.rows.length ?? 0;
  useEffect(() => {
    if (toThread.current && threadRows > 0) {
      toThread.current = false;
      thread.current?.focus();
    }
  }, [view.thread?.root, threadRows]);
  useEffect(() => {
    const focused = (e: FocusEvent) => {
      lastPane.current = paneOf(e.target as Element, "") || lastPane.current;
      const el = e.target as Element;
      session.inRoom(
        el.closest(".open-room") !== null ? "room" : el.closest('[data-pane="thread"]') !== null ? "thread" : "",
      );
    };
    const shown = () => panes.filter((p) => document.querySelector(`[data-pane="${p}"]`) !== null);
    // The message panes, by their handles; the lists, by their rows.
    const cursors: Readonly<Record<string, RefObject<TimelineNav | null>>> = { timeline, thread };
    const focusTo = (pane: string): boolean => {
      const nav = cursors[pane]?.current;
      if (nav === undefined) {
        return focusPane(pane);
      }
      nav?.focus();
      return nav !== null;
    };
    const step = (delta: number) => {
      const order = shown();
      const at = order.indexOf(paneOf(document.activeElement, lastPane.current));
      const to = order[at + delta];
      // Into the open room is into its composer, as the TUI's insert; esc goes on to the message cursor.
      return at >= 0 && to !== undefined && ((to === "timeline" && write()) || focusTo(to));
    };
    // nav moves the focused pane's cursor, opens what it is on, or goes back a pane.
    const nav = (path: string, count: number): boolean => {
      const el = document.activeElement;
      const pane = paneOf(el, lastPane.current);
      if (pane === "" || !onRow(el, pane)) {
        return false; // a control's own keys: a button's enter, a player's arrows
      }
      switch (`${pane} ${path}`) {
        case "rail nav.open":
          return focusPane("rooms");
        case "rooms nav.open": {
          const row = el instanceof HTMLElement && el.matches(paneRows.rooms ?? "") ? el : undefined;
          row?.click(); // enterRoom
          return row !== undefined;
        }
        case "rooms nav.back":
          return focusPane("rail");
        case "timeline nav.back":
          if (view.thread) {
            session.closeThread();
            return true;
          }
          return focusPane("rooms");
        case "thread nav.back":
          session.closeThread();
          return focusTo("timeline");
        case "rail nav.back":
        case "timeline nav.open":
        case "thread nav.open":
          return false;
      }
      const cursor = cursors[pane];
      return cursor === undefined ? moveRow(pane, path, count) : (cursor.current?.move(path, count) ?? false);
    };
    // message acts on the timeline's cursor, where it is a message's key's to take.
    const message = (path: string, count: number): boolean => {
      const el = document.activeElement;
      const pane = paneOf(el, lastPane.current);
      const done = onRow(el, pane) && (cursors[pane]?.current?.act(path, count) ?? false);
      toThread.current ||= done && (path === "timeline.open_thread" || path === "timeline.start_thread");
      return done;
    };
    // onRoom acts on the room under the rooms pane's cursor.
    const onRoom = (path: string): boolean => {
      const el = document.activeElement;
      const id = paneOf(el, lastPane.current) === "rooms" ? (el as HTMLElement | null)?.dataset.room : undefined;
      const room = view.rooms.find((r) => r.id === id);
      if (room === undefined || room.membership === "invite") {
        return false;
      }
      if (path === "rooms.spaces") {
        session.openFiling(room.id);
      } else if (path.startsWith("tag.")) {
        const t = view.tagKeys[Number(path.slice("tag.".length))];
        if (t === undefined) {
          return false;
        }
        session.tagRoom(room.id, t.tag);
      } else if (path === "rooms.name") {
        session.openNaming(room.id);
      } else if (path === "rooms.mark_read") {
        session.markRoomRead(room.id);
      } else if (room.isDirect) {
        session.openPerson({ roomId: room.id }, "");
      } else {
        return false;
      }
      return true;
    };
    const played = (e: Event) => {
      if (e.target instanceof HTMLMediaElement && e.target !== playing.current) {
        playing.current = e.target;
        e.target.playbackRate = view.audio.speed;
      }
    };
    const ended = (e: Event) => {
      if (e.target === playing.current) {
        playing.current = undefined;
      }
    };
    const player = (path: string): boolean => {
      const p = playing.current?.isConnected === true ? playing.current : undefined;
      const speed = (f: number) => {
        if (p !== undefined) {
          p.playbackRate = Math.min(Math.max(f, minSpeed), maxSpeed);
        }
      };
      switch (p === undefined ? "" : path) {
        case "player.play_pause":
          if (p?.paused === true) {
            void p.play();
          } else {
            p?.pause();
          }
          return true;
        case "player.back":
        case "player.forward":
          if (p !== undefined) {
            p.currentTime = Math.max(0, p.currentTime + (path === "player.back" ? -1 : 1) * view.audio.skip);
          }
          return true;
        case "player.slower":
        case "player.faster":
          speed((p?.playbackRate ?? 1) + (path === "player.slower" ? -speedStep : speedStep));
          return true;
        case "player.normal_speed":
          speed(view.audio.speed);
          return true;
        case "player.stop":
          p?.pause();
          if (p !== undefined) {
            p.currentTime = 0;
          }
          playing.current = undefined;
          return true;
      }
      return false;
    };
    // goTo goes where the ith [[keys.jump]] points: a room of the profile, or the first rail row its entry names.
    const goTo = (i: number): boolean => {
      const j = view.jumps[i];
      if (j !== undefined && view.joined.some((r) => r.id === j.room)) {
        toTimeline.current = true;
        session.open(j.room);
        return true;
      }
      const row = j === undefined || j.place === "" ? -1 : view.rail.findIndex((r) => namesRow(j.place, r));
      if (row >= 0) {
        session.selectRow(row);
      }
      return row >= 0;
    };
    const act: Readonly<Record<string, (count: number) => boolean | undefined>> = {
      focus_next: () => step(1),
      focus_prev: () => step(-1),
      jump_to: () => {
        setSwitching(true);
      },
      jump_back: () => {
        session.back();
      },
      jump_forward: () => {
        session.forward();
      },
      settings: () => {
        session.openSettings();
      },
      "rooms.spaces": () => onRoom("rooms.spaces"),
      "rooms.name": () => onRoom("rooms.name"),
      "rooms.person": () => onRoom("rooms.person"),
      "rooms.mark_read": () => onRoom("rooms.mark_read"),
      "rail.mark_read": () => {
        const row = rowsOf("rail").indexOf(document.activeElement as HTMLElement);
        if (row >= 0) {
          session.askMarkRead(row);
        }
        return row >= 0;
      },
      help: () => {
        setHelping(true);
      },
    };
    const key = (e: KeyboardEvent) => {
      const name = keyName(e);
      if (name === undefined || e.defaultPrevented) {
        return; // a modifier alone, or a press a field took for itself
      }
      const paths = keymap.press(name, typing(document.activeElement));
      setPending(keymap.pending);
      // The first action that takes the press acts; one passed on by all (false) keeps what it does by itself.
      const done = paths.some(
        (path) =>
          (
            act[path] ??
            ((count) =>
              path.startsWith("jump.")
                ? goTo(Number(path.slice("jump.".length)))
                : path.startsWith("tag.")
                  ? onRoom(path)
                  : path.startsWith("player.")
                    ? player(path)
                    : path.startsWith("timeline.")
                      ? message(path, count)
                      : nav(path, count))
          )(keymap.count) !== false,
      );
      if (done || keymap.pending !== "") {
        e.preventDefault();
      }
    };
    window.addEventListener("keydown", key);
    window.addEventListener("focusin", focused);
    // Media events do not bubble: caught on the way down.
    document.addEventListener("play", played, true);
    document.addEventListener("ended", ended, true);
    return () => {
      window.removeEventListener("keydown", key);
      window.removeEventListener("focusin", focused);
      document.removeEventListener("play", played, true);
      document.removeEventListener("ended", ended, true);
    };
  }, [session, keymap, view.thread, view.rooms, view.jumps, view.tagKeys, view.joined, view.rail, view.audio]);
  // The open thread's row, when the list shows it, is where the list's cursor is; else the open room's.
  const threadListed = view.list.some(
    (r) => r.kind === "thread" && r.room.id === view.open && r.thread.root === view.thread?.root,
  );
  const columns = ["columns"];
  if (view.thread && view.timeline) {
    columns.push("with-thread");
  }
  if (unfolded) {
    columns.push("unfolded");
  }
  // Searching shows its results in the left column, so it stays while there are any.
  if (folded === "places" || (folded === "rooms" && view.search)) {
    columns.push("folded");
  } else if (folded === "rooms") {
    columns.push("folded-all");
  }
  if (!view.timeline) {
    columns.push("no-room");
  }
  return (
    <>
      <TopBar view={view} session={session}>
        {pending !== "" && (
          <kbd className="keys-pending" aria-live="polite">
            {pending}…
          </kbd>
        )}
        <button
          type="button"
          className="fold"
          aria-label="Rooms and places"
          aria-expanded={narrow() ? unfolded : folded === "none"}
          title={{ none: "Hide places", places: "Hide the room list too", rooms: "Show places and rooms" }[folded]}
          onClick={() => {
            if (narrow()) {
              setUnfoldedAt(unfolded ? undefined : here);
            } else {
              setFolded(({ none: "places", places: "rooms", rooms: "none" } as const)[folded]);
            }
          }}
        >
          <span className="fold-glyph">☰</span>
          {/* The wide window's columns as bars: places, rooms, the room; a folded one hollow. */}
          <span className="fold-icon" aria-hidden="true">
            <span className={folded === "none" ? "shown" : ""} />
            <span className={folded === "rooms" ? "" : "shown"} />
            <span className="shown room" />
          </span>
        </button>
        <nav className="find" aria-label="Find">
          <button
            type="button"
            aria-label="Back"
            title={`Back (${(view.keys.jump_back ?? []).join(", ")})`}
            disabled={!view.back}
            onClick={() => {
              session.back();
            }}
          >
            ←
          </button>
          <button
            type="button"
            aria-label="Forward"
            title={`Forward (${(view.keys.jump_forward ?? []).join(", ")})`}
            disabled={!view.forward}
            onClick={() => {
              session.forward();
            }}
          >
            →
          </button>
          <SearchBar open={view.open !== ""} session={session} />
          <button
            type="button"
            className="go-to"
            title="ctrl+K"
            onClick={() => {
              setSwitching(true);
            }}
          >
            Go to
          </button>
          <button
            type="button"
            className="go-to"
            title="Every card in the phonebook"
            onClick={() => {
              session.openPeople();
            }}
          >
            People
          </button>
        </nav>
      </TopBar>
      {view.problem && <p className="problem">{view.problem}</p>}
      {view.offers.note && (
        <p className="banner" role="status">
          {view.offers.note}{" "}
          <button
            type="button"
            aria-label="Dismiss"
            onClick={() => {
              session.forgetOfferNote();
            }}
          >
            ×
          </button>
        </p>
      )}
      {view.notice && (
        <p className={`banner note-${view.notice.level}`} role={view.notice.level === "error" ? "alert" : "status"}>
          <span dir="auto">{view.notice.text}</span>{" "}
          <button
            type="button"
            aria-label="Dismiss"
            onClick={() => {
              session.forgetNotice();
            }}
          >
            ×
          </button>
        </p>
      )}
      <Banner link={view.link} status={view.status} bridge={view.bridge} />
      {helping && (
        <HelpOverlay
          keys={keys}
          sections={[
            ...helpSections,
            {
              title: "Go to (your [[keys.jump]])",
              rows: view.jumps.map((j, i) => ({
                paths: [`jump.${String(i)}`],
                what: j.room === "" ? j.place : (view.joined.find((r) => r.id === j.room)?.name ?? j.room),
              })),
            },
            {
              title: "File the room into a tag, or out (your [[keys.tag]])",
              rows: view.tagKeys.map((t, i) => ({ paths: [`tag.${String(i)}`], what: t.tag })),
            },
          ]}
          close={() => {
            setHelping(false);
          }}
        />
      )}
      {switching && (
        <Switcher
          view={view}
          session={session}
          close={() => {
            setSwitching(false);
          }}
        />
      )}
      <div className={columns.join(" ")}>
        <div className="sidebar">
          {view.profiles.length > 1 && (
            <div className="sidebar-head">
              <Profile view={view} session={session} />
            </div>
          )}
          <div className="sidebar-columns">
            <Rail view={view} session={session} />
            {view.search ? (
              <SearchResults search={view.search} rooms={view.joined} session={session} />
            ) : (
              <section className="rooms" aria-label="Rooms" data-pane="rooms">
                {view.invites.length > 0 && <Invites invites={view.invites} session={session} />}
                <ul>
                  {view.list.map((row) => {
                    const r = row.room;
                    switch (row.kind) {
                      case "room":
                        return (
                          <RoomRow
                            key={r.id}
                            room={r}
                            silenced={row.silenced}
                            unread={view.unread.get(r.id)}
                            open={r.id === view.open && !threadListed}
                            theme={view.theme}
                            session={session}
                            onEnter={enterRoom}
                          />
                        );
                      case "thread":
                        return (
                          <ThreadRow
                            key={`${r.id} ${row.thread.root}`}
                            room={r.id}
                            thread={row.thread}
                            marker={view.threads?.threadsMarker ?? ""}
                            open={r.id === view.open && row.thread.root === view.thread?.root}
                            session={session}
                            onEnter={enterThread}
                          />
                        );
                      case "more":
                        return (
                          <li key={`${r.id} more`} className="thread more">
                            <span className="marker" aria-hidden="true">
                              {view.threads?.threadsMarker}
                            </span>
                            <span className="label">
                              +{row.more} more {row.more === 1 ? "thread" : "threads"}
                            </span>
                          </li>
                        );
                    }
                  })}
                </ul>
              </section>
            )}
          </div>
          {view.status && (
            <details className="connection">
              <summary>Connection</summary>
              <div className="connection-panel">
                <Status status={view.status} />
                <Where view={view} />
              </div>
            </details>
          )}
        </div>
        {view.timeline ? (
          <div className="open-room" data-pane="timeline">
            <RoomHeader
              theme={view.theme}
              id={view.open}
              room={view.joined.find((r) => r.id === view.open) ?? view.rooms.find((r) => r.id === view.open)}
              session={session}
            />
            <TimelinePane
              ref={timeline}
              t={view.timeline}
              unreadLine={view.unreadLine}
              composer={view.composer}
              keys={view.keys}
              faces={{ theme: view.theme, slots: view.slots, pictures: view.pictures }}
              session={session}
            />
            {view.composer && (
              <ComposerPane
                c={view.composer}
                keys={view.keys}
                room={view.rooms.find((r) => r.id === view.open)}
                inThread={view.thread !== undefined}
                session={session}
                onLeave={() => {
                  timeline.current?.focus();
                }}
              />
            )}
          </div>
        ) : (
          <div className="open-room empty">
            <p className="detail">Pick a room, or press ctrl+K to go to one.</p>
          </div>
        )}
        {view.thread && view.timeline && (
          <ThreadPane
            ref={thread}
            thread={view.thread}
            t={view.timeline}
            composer={view.composer}
            keys={view.keys}
            faces={{ theme: view.theme, slots: view.slots, pictures: view.pictures }}
            session={session}
          />
        )}
      </div>
    </>
  );
}

/**
 * RoomHeader names the open room above its messages: its face, name, network and topic,
 * and files it into tags and spaces.
 */
function RoomHeader({
  theme,
  id,
  room,
  session,
}: {
  theme: ThemeResponse | undefined;
  id: string;
  room: Room | undefined;
  session: Session;
}) {
  const name = room?.name || id;
  const mark = networkMark(room?.network ?? "");
  const color = room && roomColor(theme, room);
  return (
    <header className="room-header">
      <Avatar id={id} name={name} picture={drawsPictures(theme) ? roomPicture(room) : undefined} />
      <h2 dir="auto">{name}</h2>
      {mark && <span className={`net ${color ? colorClass(color) : `net-${mark.slug}`}`}>{room?.network}</span>}
      {room?.topic ? (
        <span className="topic detail" dir="auto" title={room.topic}>
          {room.topic}
        </span>
      ) : (
        <span className="topic" />
      )}
      {room?.isDirect === true && room.membership !== "invite" && (
        <button
          type="button"
          className="file-room"
          title="Their phonebook card"
          onClick={() => {
            session.openPerson({ roomId: id }, "");
          }}
        >
          Card
        </button>
      )}
      {room && room.membership !== "invite" && (
        <button
          type="button"
          className="file-room"
          title="File this room into tags and spaces"
          onClick={() => {
            session.openFiling(id);
          }}
        >
          Tags and spaces
        </button>
      )}
    </header>
  );
}

function Picker({ profiles, session }: { profiles: readonly string[]; session: Session }) {
  return (
    <section className="picker">
      <h2>Which profile should this window run in?</h2>
      <p className="detail">It is remembered for the next start; the profile menu changes it.</p>
      <ul>
        {profiles.map((p) => (
          <li key={p}>
            <button
              type="button"
              onClick={() => {
                void session.choose(p);
              }}
            >
              {p}
            </button>
          </li>
        ))}
      </ul>
    </section>
  );
}

function SeatState({ seat, profile, session }: { seat: Seat; profile: string; session: Session }) {
  const takeOver = (label: string) => (
    <button
      type="button"
      onClick={() => {
        session.takeOver();
      }}
    >
      {label}
    </button>
  );
  switch (seat.state) {
    case "none":
    case "asking":
    case "seated":
      return <p className="banner">Opening {profile}…</p>;
    case "taken":
      return (
        <p className="banner waiting">
          {profile} is open in another window{seat.holder && <> ({holderText(seat.holder)})</>}. {takeOver("Take over")}
        </p>
      );
    case "steppedAside":
      return (
        <p className="banner waiting">
          {profile} was opened in another window. {takeOver("Take it back")}
        </p>
      );
  }
}

/** holderText reads like "pid 4312 on laptop, since 10:41", as the TUI says it. */
function holderText(h: SeatHolder): string {
  let text = `pid ${String(h.pid)}`;
  if (h.tty) {
    text += ` on ${h.tty}`;
  }
  if (h.host) {
    text += ` (${h.host})`;
  }
  if (h.sinceUnixMs > 0n) {
    text += `, since ${clock.format(new Date(Number(h.sinceUnixMs)))}`;
  }
  return text;
}

/** Rail draws kithd's rail: its rows in order, the one the window is on marked. */
function Rail({ view, session }: { view: View; session: Session }) {
  return (
    <nav className="rail" aria-label="Places" data-pane="rail">
      <ul>
        {view.rail.map((row, i) => {
          const { count, mentions } = rowUnread(row, view.unread, view.silenced);
          return (
            <li key={`${row.key}\u0000${row.space}`} className={row.sepAfter ? "place separated" : "place"}>
              <button
                type="button"
                aria-current={i === view.row}
                onClick={() => {
                  session.selectRow(i);
                }}
              >
                <span className="label" dir="auto">
                  {row.label}
                </span>
                {row.countInLabel && <span className="detail"> {new Set(row.rooms).size}</span>}
                {count > 0 && <Badge count={count} mentions={mentions} />}
              </button>
            </li>
          );
        })}
      </ul>
    </nav>
  );
}

function Invites({ invites, session }: { invites: readonly Room[]; session: Session }) {
  return (
    <section className="invites" aria-label="Invitations">
      <h2>Invitations</h2>
      <ul>
        {invites.map((r) => (
          <li key={r.id}>
            <span className="invite-room">
              <span dir="auto">{r.name || r.id}</span>
              {r.invitedBy && (
                <span className="detail" dir="auto">
                  {" "}
                  from {r.invitedBy}
                </span>
              )}
            </span>
            <span className="invite-actions">
              <button
                type="button"
                onClick={() => {
                  void session.accept(r.id);
                }}
              >
                Accept
              </button>
              <button
                type="button"
                onClick={() => {
                  void session.decline(r.id);
                }}
              >
                Decline
              </button>
            </span>
          </li>
        ))}
      </ul>
    </section>
  );
}

function RoomRow({
  room,
  silenced,
  unread,
  open,
  theme,
  session,
  onEnter,
}: {
  room: Room;
  silenced: boolean;
  unread: Unread | undefined;
  open: boolean;
  theme: ThemeResponse | undefined;
  session: Session;
  onEnter: (room: string) => void;
}) {
  const { count, mentions } = unreadOf(unread);
  const name = room.name || room.id;
  const mark = networkMark(room.network);
  const color = roomColor(theme, room);
  return (
    <li className={count > 0 || unread?.marked ? "room unread" : "room"}>
      <button
        type="button"
        aria-current={open}
        data-room={room.id}
        title="Right-click to file it into tags and spaces"
        onFocus={() => {
          session.cursorOn(room.id, "");
          clearTimeout(peeking);
          peeking = setTimeout(() => {
            session.peek(room.id);
          }, peekAfter);
        }}
        onClick={() => {
          onEnter(room.id);
        }}
        onContextMenu={(e) => {
          e.preventDefault();
          session.openFiling(room.id);
        }}
      >
        <Avatar id={room.id} name={name} small picture={drawsPictures(theme) ? roomPicture(room) : undefined} />
        <span className="label" dir="auto">
          {name}
        </span>
        {mark && (
          <span className={`net ${color ? colorClass(color) : `net-${mark.slug}`}`} title={room.network}>
            {mark.abbr}
          </span>
        )}
        {count > 0 &&
          (silenced ? (
            <span className="badge silent" title={`${String(count)} unread`}>
              {count}
            </span>
          ) : (
            <Badge count={count} mentions={mentions} />
          ))}
      </button>
    </li>
  );
}

/**
 * ThreadRow is one of a room's threads, under its room in the list: the marker, its root's
 * text, and its unread replies. Resting on it shows its room; it reads only the thread.
 */
function ThreadRow({
  room,
  thread,
  marker,
  open,
  session,
  onEnter,
}: {
  room: string;
  thread: ThreadUnread;
  marker: string;
  open: boolean;
  session: Session;
  onEnter: (room: string, root: string) => void;
}) {
  const title = thread.title.replace(/\s+/g, " ").trim() || "an older thread";
  return (
    <li className={thread.unread > 0n ? "thread unread" : "thread"}>
      <button
        type="button"
        aria-current={open}
        data-thread={thread.root}
        data-thread-room={room}
        onFocus={() => {
          session.cursorOn(room, thread.root);
          clearTimeout(peeking);
          peeking = setTimeout(() => {
            session.peek(room);
          }, peekAfter);
        }}
        onClick={() => {
          onEnter(room, thread.root);
        }}
      >
        <span className="marker" aria-hidden="true">
          {marker}
        </span>
        <span className="label" dir="auto">
          {title}
        </span>
        {thread.unread > 0n && <Badge count={Number(thread.unread)} mentions={Number(thread.mentions)} />}
      </button>
    </li>
  );
}

/** Badge counts what is unread; a count that names you is set apart, by its color and an @. */
function Badge({ count, mentions }: { count: number; mentions: number }) {
  return mentions > 0 ? (
    <span className="badge mention" title={`${String(count)} unread, ${String(mentions)} naming you`}>
      @{count}
    </span>
  ) : (
    <span className="badge" title={`${String(count)} unread`}>
      {count}
    </span>
  );
}

function Banner({ link, status, bridge }: { link: Link; status: StatusResponse | undefined; bridge: string }) {
  if (status?.waiting) {
    return <p className="banner waiting">kithd is waiting: {status.waiting}</p>;
  }
  const kithd = bridge === "" ? "kithd" : `kithd on bridge ${bridge}`;
  switch (link.state) {
    case "connecting":
      return <p className="banner">Connecting to {kithd}…</p>;
    case "detached":
      return (
        <p className="banner lost">
          {kithd} is not reachable ({link.error}). Trying again.
        </p>
      );
    case "attached":
      return null;
  }
}

function Status({ status }: { status: StatusResponse }) {
  return (
    <section>
      <dl>
        <dt>Cache</dt>
        <dd>{status.ready ? "synced" : "not synced yet"}</dd>
        <dt>Last sync</dt>
        <dd>{status.syncedAt ? when.format(timestampDate(status.syncedAt)) : "never"}</dd>
        {status.lastError && (
          <>
            <dt>Last error</dt>
            <dd>{status.lastError}</dd>
          </>
        )}
      </dl>
      <table>
        <thead>
          <tr>
            <th>Network</th>
            <th>Account</th>
            <th>State</th>
          </tr>
        </thead>
        <tbody>
          {status.networks.map((n) => (
            <Network key={`${n.network}/${n.account}`} network={n} />
          ))}
        </tbody>
      </table>
    </section>
  );
}

function Network({ network }: { network: NetworkStatus }) {
  return (
    <tr>
      <td>{network.network}</td>
      <td dir="auto">{network.account}</td>
      <td>
        {phases[network.phase]}
        {network.detail && <span className="detail">: {network.detail}</span>}
      </td>
    </tr>
  );
}

/** The theme's stylesheet, beside styles.css (a constructed sheet: the window's policy allows no <style>). */
let sheet: CSSStyleSheet | undefined;

/** paint draws the window in theme, and has main draw it light, dark or as the system is; none read yet leaves the built-in one. */
function paint(theme: ThemeResponse | undefined): void {
  if (theme === undefined) {
    return;
  }
  if (sheet === undefined) {
    sheet = new CSSStyleSheet();
    document.adoptedStyleSheets = [...document.adoptedStyleSheets, sheet];
  }
  sheet.replaceSync(themeCSS(theme));
  window.kith.themeMode(modeOf(theme));
}
