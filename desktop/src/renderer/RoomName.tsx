// A room's name of your own (rooms.name, as the TUI's): typed over the one it shows, and
// written by kithd into [[display.name]]; an empty one gives it its own name back. And
// the key sequence that goes to a room (/shortcut), asked the same way.
import { useState } from "react";

import { bound, type Keys } from "./keymap";
import type { Session } from "./session";

/** Asked is one line asked in a dialog, starting from value; enter takes it, esc puts it away. */
function Asked({
  title,
  label,
  detail,
  action,
  value,
  keys,
  onDone,
  onCancel,
}: {
  title: string;
  label: string;
  detail: string;
  action: string;
  value: string;
  keys: Keys;
  onDone: (typed: string) => void;
  onCancel: () => void;
}) {
  const [typed, setTyped] = useState(value);
  return (
    <div className="offer-backdrop">
      <section className="offer" role="dialog" aria-modal="true" aria-label={title}>
        <h2>{title}</h2>
        <input
          autoFocus
          dir="auto"
          aria-label={label}
          value={typed}
          onChange={(e) => {
            setTyped(e.target.value);
          }}
          onFocus={(e) => {
            e.target.select();
          }}
          onKeyDown={(e) => {
            if (bound(keys, "prompt.submit", e)) {
              e.preventDefault();
              onDone(typed);
            } else if (bound(keys, "prompt.cancel", e)) {
              e.preventDefault();
              onCancel();
            }
          }}
        />
        <p className="detail">{detail}</p>
        <p className="offer-actions">
          <button type="button" onClick={onCancel}>
            Cancel
          </button>
          <button
            type="button"
            className="primary"
            onClick={() => {
              onDone(typed);
            }}
          >
            {action}
          </button>
        </p>
      </section>
    </div>
  );
}

export function RoomNameDialog({ naming, keys, session }: { naming: { name: string }; keys: Keys; session: Session }) {
  return (
    <Asked
      title="Name this room"
      label="Its name, for you; empty gives it its own back"
      detail="Only you see it. Leave it empty for the room's own name."
      action="Name it"
      value={naming.name}
      keys={keys}
      onDone={(typed) => {
        session.nameRoom(typed);
      }}
      onCancel={() => {
        session.closeNaming();
      }}
    />
  );
}

export function ShortcutDialog({
  shortcut,
  keys,
  session,
}: {
  shortcut: { room: string; keys: string };
  keys: Keys;
  session: Session;
}) {
  return (
    <Asked
      title="Shortcut to this room"
      label="Its key sequence; empty unbinds it"
      detail="Key names as [keys] spells them, a space between presses: g w, ctrl+1. Leave it empty to unbind."
      action="Bind it"
      value={shortcut.keys}
      keys={keys}
      onDone={(typed) => {
        void session.bindShortcut(shortcut.room, typed);
      }}
      onCancel={() => {
        session.closeShortcut();
      }}
    />
  );
}
