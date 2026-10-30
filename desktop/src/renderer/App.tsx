import { timestampDate } from "@bufbuild/protobuf/wkt";
import { useEffect, useState, useSyncExternalStore } from "react";

import { NetworkPhase, type NetworkStatus, type SeatHolder, type StatusResponse } from "../gen/backend/v1/services_pb";
import type { Room, Unread } from "../gen/backend/v1/types_pb";
import { unreadOf } from "./rooms";
import { type Link, type Seat, Session, type View } from "./session";
import { TimelinePane } from "./TimelinePane";

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

  return (
    <main>
      <header>
        <h1>kith</h1>
        {view.profiles.length > 1 && view.profile !== "" && (
          <select
            aria-label="Profile"
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
        )}
      </header>
      {view.problem && <p className="problem">{view.problem}</p>}
      <Body view={view} session={session} />
      {view.socket && (
        <footer>
          kithd at <code>{view.socket}</code>, through kith-relay
        </footer>
      )}
    </main>
  );
}

function Body({ view, session }: { view: View; session: Session }) {
  if (view.profile === "") {
    return view.profiles.length > 0 ? <Picker profiles={view.profiles} session={session} /> : null;
  }
  if (view.seat.state !== "seated") {
    return <SeatState seat={view.seat} profile={view.profile} session={session} />;
  }
  return (
    <>
      <Banner link={view.link} status={view.status} />
      <div className="columns">
        {/* The rail (places, tags, spaces) comes with kithd's Rail. */}
        <nav className="rail" aria-label="Places">
          <span className="place">All rooms</span>
        </nav>
        <section className="rooms" aria-label="Rooms">
          {view.invites.length > 0 && <Invites invites={view.invites} session={session} />}
          <ul>
            {view.rooms.map((r) => (
              <RoomRow key={r.id} room={r} unread={view.unread.get(r.id)} open={r.id === view.open} session={session} />
            ))}
          </ul>
        </section>
        {view.timeline && <TimelinePane t={view.timeline} unreadLine={view.unreadLine} session={session} />}
      </div>
      {view.status && (
        <details>
          <summary>Connection</summary>
          <Status status={view.status} />
        </details>
      )}
    </>
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

function Invites({ invites, session }: { invites: readonly Room[]; session: Session }) {
  return (
    <section className="invites" aria-label="Invitations">
      <h2>Invitations</h2>
      <ul>
        {invites.map((r) => (
          <li key={r.id}>
            <span dir="auto">{r.name || r.id}</span>
            {r.invitedBy && (
              <span className="detail" dir="auto">
                {" "}
                from {r.invitedBy}
              </span>
            )}{" "}
            <button
              type="button"
              onClick={() => {
                void session.accept(r.id);
              }}
            >
              Accept
            </button>{" "}
            <button
              type="button"
              onClick={() => {
                void session.decline(r.id);
              }}
            >
              Decline
            </button>
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
  session,
}: {
  room: Room;
  unread: Unread | undefined;
  open: boolean;
  session: Session;
}) {
  const { count, mentions } = unreadOf(unread);
  return (
    <li className={count > 0 || unread?.marked ? "room unread" : "room"}>
      <button
        type="button"
        aria-current={open}
        onClick={() => {
          session.open(room.id);
        }}
      >
        <span dir="auto">{room.name || room.id}</span>
        {count > 0 && <span className={mentions > 0 ? "badge mention" : "badge"}>{count}</span>}
      </button>
    </li>
  );
}

function Banner({ link, status }: { link: Link; status: StatusResponse | undefined }) {
  if (status?.waiting) {
    return <p className="banner waiting">kithd is waiting: {status.waiting}</p>;
  }
  switch (link.state) {
    case "connecting":
      return <p className="banner">Connecting to kithd…</p>;
    case "detached":
      return <p className="banner lost">kithd is not reachable ({link.error}). Trying again.</p>;
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
