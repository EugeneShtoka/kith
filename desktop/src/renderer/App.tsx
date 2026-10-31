import { timestampDate } from "@bufbuild/protobuf/wkt";
import { type ReactNode, useEffect, useState, useSyncExternalStore } from "react";

import { NetworkPhase, type NetworkStatus, type SeatHolder, type StatusResponse } from "../gen/backend/v1/services_pb";
import type { Room, Unread } from "../gen/backend/v1/types_pb";
import type { ThemeResponse } from "../gen/device/v1/device_pb";
import { Avatar } from "./Avatar";
import { ComposerPane } from "./ComposerPane";
import { networkMark } from "./faces";
import { FilingDialog } from "./Filing";
import { OfferDialog } from "./Offer";
import { colorClass, themeCSS, modeOf, roomColor } from "./theme";
import { rowUnread } from "./rail";
import { unreadOf } from "./rooms";
import { type Link, type Seat, Session, type View } from "./session";
import { SearchBar, SearchResults, Switcher } from "./Find";
import { SettingsPage } from "./SettingsPage";
import { ThreadPane } from "./ThreadPane";
import { TimelinePane } from "./TimelinePane";
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
      {seated && view.filing && <FilingDialog filing={view.filing} session={session} />}
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

/** TopBar runs across the window's top: kith, what the screen puts there, and Settings. */
function TopBar({ view, session, children }: { view: View; session: Session; children?: ReactNode }) {
  return (
    <header className="topbar">
      <h1>kith</h1>
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
 * with its composer docked under it, and a thread beside it. In a narrow window the
 * left column folds away behind a toggle.
 */
function Seated({ view, session }: { view: View; session: Session }) {
  const [switching, setSwitching] = useState(false);
  // The left column unfolded over a narrow window: until a room or a place is picked.
  const here = `${view.open}\u0000${String(view.row)}`;
  const [unfoldedAt, setUnfoldedAt] = useState<string | undefined>(undefined);
  const unfolded = unfoldedAt === here;
  useEffect(() => {
    const key = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && !e.altKey && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setSwitching(true);
      } else if (e.altKey && !e.ctrlKey && (e.key === "ArrowLeft" || e.key === "ArrowRight")) {
        e.preventDefault();
        if (e.key === "ArrowLeft") {
          session.back();
        } else {
          session.forward();
        }
      }
    };
    window.addEventListener("keydown", key);
    return () => {
      window.removeEventListener("keydown", key);
    };
  }, [session]);
  const columns = ["columns"];
  if (view.thread && view.timeline) {
    columns.push("with-thread");
  }
  if (unfolded) {
    columns.push("unfolded");
  }
  if (!view.timeline) {
    columns.push("no-room");
  }
  return (
    <>
      <TopBar view={view} session={session}>
        <button
          type="button"
          className="fold"
          aria-label="Rooms and places"
          aria-expanded={unfolded}
          onClick={() => {
            setUnfoldedAt(unfolded ? undefined : here);
          }}
        >
          ☰
        </button>
        <nav className="find" aria-label="Find">
          <button
            type="button"
            aria-label="Back"
            title="Back (alt+←)"
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
            title="Forward (alt+→)"
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
      {view.filingNote && (
        <p className="banner" role="status">
          <span dir="auto">{view.filingNote}</span>{" "}
          <button
            type="button"
            aria-label="Dismiss"
            onClick={() => {
              session.forgetFilingNote();
            }}
          >
            ×
          </button>
        </p>
      )}
      <Banner link={view.link} status={view.status} bridge={view.bridge} />
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
              <section className="rooms" aria-label="Rooms">
                {view.invites.length > 0 && <Invites invites={view.invites} session={session} />}
                <ul>
                  {view.rooms.map((r) => (
                    <RoomRow
                      key={r.id}
                      room={r}
                      unread={view.unread.get(r.id)}
                      open={r.id === view.open}
                      theme={view.theme}
                      session={session}
                    />
                  ))}
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
          <div className="open-room">
            <RoomHeader
              theme={view.theme}
              id={view.open}
              room={view.joined.find((r) => r.id === view.open) ?? view.rooms.find((r) => r.id === view.open)}
              session={session}
            />
            <TimelinePane
              t={view.timeline}
              unreadLine={view.unreadLine}
              composer={view.composer}
              faces={{ theme: view.theme, slots: view.slots }}
              session={session}
            />
            {view.composer && (
              <ComposerPane
                c={view.composer}
                room={view.rooms.find((r) => r.id === view.open)}
                inThread={view.thread !== undefined}
                session={session}
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
            thread={view.thread}
            t={view.timeline}
            composer={view.composer}
            faces={{ theme: view.theme, slots: view.slots }}
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
      <Avatar id={id} name={name} />
      <h2 dir="auto">{name}</h2>
      {mark && <span className={`net ${color ? colorClass(color) : `net-${mark.slug}`}`}>{room?.network}</span>}
      {room?.topic ? (
        <span className="topic detail" dir="auto" title={room.topic}>
          {room.topic}
        </span>
      ) : (
        <span className="topic" />
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
    <nav className="rail" aria-label="Places">
      <ul>
        {view.rail.map((row, i) => {
          const { count, mentions } = rowUnread(row, view.unread);
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
  unread,
  open,
  theme,
  session,
}: {
  room: Room;
  unread: Unread | undefined;
  open: boolean;
  theme: ThemeResponse | undefined;
  session: Session;
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
        title="Right-click to file it into tags and spaces"
        onClick={() => {
          session.open(room.id);
        }}
        onContextMenu={(e) => {
          e.preventDefault();
          session.openFiling(room.id);
        }}
      >
        <Avatar id={room.id} name={name} small />
        <span className="label" dir="auto">
          {name}
        </span>
        {mark && (
          <span className={`net ${color ? colorClass(color) : `net-${mark.slug}`}`} title={room.network}>
            {mark.abbr}
          </span>
        )}
        {count > 0 && <Badge count={count} mentions={mentions} />}
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
