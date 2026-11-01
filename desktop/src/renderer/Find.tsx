// Finding things: the search bar and its hits, and the switcher (ctrl+K).
import { timestampDate } from "@bufbuild/protobuf/wkt";
import { type ReactNode, useEffect, useState } from "react";

import type { Room, SearchHit } from "../gen/backend/v1/types_pb";
import type { Search, SearchScope, Session, View } from "./session";
import { type Choice, choices, hasFieldTerm, matching, personChoices } from "./switcher";

const when = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" });

/** How many rows the switcher lists at once: typing narrows it. */
const switcherRows = 50;

/** SearchBar asks for a search: words, with from:, since: and until: as the TUI reads them. */
export function SearchBar({ open, session }: { open: boolean; session: Session }) {
  const [query, setQuery] = useState("");
  const [scope, setScope] = useState<SearchScope>("all");
  return (
    <form
      className="search"
      role="search"
      onSubmit={(e) => {
        e.preventDefault();
        session.search(query, scope === "room" && !open ? "all" : scope);
      }}
    >
      <input
        type="search"
        aria-label="Search messages"
        placeholder="Search: words, from:, since:, until:"
        value={query}
        onChange={(e) => {
          setQuery(e.target.value);
        }}
      />
      <select
        aria-label="Where"
        value={scope}
        onChange={(e) => {
          setScope(e.target.value as SearchScope);
        }}
      >
        <option value="room" disabled={!open}>
          This room
        </option>
        <option value="row">This place</option>
        <option value="all">Everywhere</option>
      </select>
      <button type="submit">Search</button>
    </form>
  );
}

/** SearchResults lists a search's hits, newest first; picking one jumps to its message. */
export function SearchResults({
  search,
  rooms,
  session,
}: {
  search: Search;
  rooms: readonly Room[];
  session: Session;
}) {
  const names = new Map(rooms.map((r) => [r.id, r.name || r.id]));
  const counted = search.busy
    ? "Searching"
    : search.note || (search.hits.length === 1 ? "1 message" : `${String(search.hits.length)} messages`);
  const said = search.about ? `Everything ${search.about} said: ${counted}` : counted;
  return (
    <section className="rooms search-results" aria-label="Search results">
      <header>
        <span className="detail" dir="auto">
          {said}
        </span>
        <button
          type="button"
          onClick={() => {
            session.closeSearch();
          }}
        >
          Close
        </button>
      </header>
      <ul>
        {search.hits.map((h) => (
          <li key={`${h.roomId}\u0000${h.eventId}`} className="hit">
            <button
              type="button"
              onClick={() => {
                session.jump(h.roomId, h.eventId);
              }}
            >
              <Hit hit={h} room={names.get(h.roomId) ?? h.roomId} />
            </button>
          </li>
        ))}
      </ul>
    </section>
  );
}

function Hit({ hit, room }: { hit: SearchHit; room: string }) {
  return (
    <>
      <span className="detail">
        <span dir="auto">{room}</span> · <span dir="auto">{hit.senderName || hit.sender}</span>
        {hit.timestamp && <> · {when.format(timestampDate(hit.timestamp))}</>}
      </span>
      <span className="snippet" dir="auto">
        {snippet(hit.snippet)}
        {hit.fileName && <span className="detail"> ({hit.fileName})</span>}
      </span>
    </>
  );
}

/** snippet draws a hit's excerpt, each matched run (between \x01 and \x02) marked. */
function snippet(text: string): ReactNode[] {
  return text.split("\u0001").flatMap((part, i) => {
    if (i === 0) {
      return [part];
    }
    const [hit = "", rest = ""] = part.split("\u0002", 2);
    return [<mark key={i}>{hit}</mark>, rest];
  });
}

/**
 * Switcher is the ctrl+K box: type part of a name, move with the arrows, enter to go;
 * escape, or a click outside, closes it.
 */
export function Switcher({ view, session, close }: { view: View; session: Session; close: () => void }) {
  const [typed, setTyped] = useState("");
  const [at, setAt] = useState(0);
  // The people a field term lists, for the text they were listed for.
  const [people, setPeople] = useState<{ typed: string; rows: Choice[] }>({ typed: "", rows: [] });
  useEffect(() => {
    if (!hasFieldTerm(typed)) {
      return;
    }
    let current = true;
    session.listPeople(typed).then(
      (listed) => {
        if (current) {
          setPeople({ typed, rows: personChoices(listed) });
        }
      },
      () => {
        if (current) {
          setPeople({ typed, rows: [] });
        }
      },
    );
    return () => {
      current = false;
    };
  }, [typed, session]);
  const listed = hasFieldTerm(typed) && people.typed === typed ? people.rows : [];
  const shown = [...matching(choices(view.joined, view.unread, view.rail), typed), ...listed].slice(0, switcherRows);
  const go = (c: Choice | undefined) => {
    if (c !== undefined) {
      if ("room" in c.go) {
        session.open(c.go.room);
      } else if ("person" in c.go) {
        session.goToPerson(c.go.person);
      } else {
        session.selectRow(c.go.row);
      }
    }
    close();
  };
  return (
    <div className="switcher-backdrop" onClick={close}>
      <div
        className="switcher"
        role="dialog"
        aria-modal="true"
        aria-label="Go to"
        onClick={(e) => {
          e.stopPropagation();
        }}
      >
        <input
          autoFocus
          aria-label="Go to a room, a person or a place; a field (role:attorney) lists people"
          placeholder="A room, a person, a place, or role:attorney"
          value={typed}
          onChange={(e) => {
            setTyped(e.target.value);
            setAt(0);
          }}
          onKeyDown={(e) => {
            if (e.key === "ArrowDown" || e.key === "ArrowUp") {
              e.preventDefault();
              const n = shown.length;
              setAt(n === 0 ? 0 : (at + (e.key === "ArrowDown" ? 1 : n - 1)) % n);
            } else if (e.key === "Enter") {
              e.preventDefault();
              go(shown[at]);
            } else if (e.key === "Escape") {
              e.preventDefault();
              close();
            }
          }}
        />
        <ul role="listbox" aria-label="Places to go">
          {shown.map((c, i) => (
            <li key={c.key} role="option" aria-selected={i === at}>
              <button
                type="button"
                onClick={() => {
                  go(c);
                }}
              >
                <span dir="auto">{c.label}</span>
                {c.detail && <span className="detail"> {c.detail}</span>}
              </button>
            </li>
          ))}
        </ul>
      </div>
    </div>
  );
}
