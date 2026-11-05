// A room's name of your own (rooms.name, as the TUI's): typed over the one it shows, and
// written by kithd into [[display.name]]; an empty one gives it its own name back.
import { useState } from "react";

import { bound, type Keys } from "./keymap";
import type { Session } from "./session";

export function RoomNameDialog({ naming, keys, session }: { naming: { name: string }; keys: Keys; session: Session }) {
  const [typed, setTyped] = useState(naming.name);
  return (
    <div className="offer-backdrop">
      <section className="offer" role="dialog" aria-modal="true" aria-label="Name this room">
        <h2>Name this room</h2>
        <input
          autoFocus
          dir="auto"
          aria-label="Its name, for you; empty gives it its own back"
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
              session.nameRoom(typed);
            } else if (bound(keys, "prompt.cancel", e)) {
              e.preventDefault();
              session.closeNaming();
            }
          }}
        />
        <p className="detail">Only you see it. Leave it empty for the room&apos;s own name.</p>
        <p className="offer-actions">
          <button
            type="button"
            onClick={() => {
              session.closeNaming();
            }}
          >
            Cancel
          </button>
          <button
            type="button"
            className="primary"
            onClick={() => {
              session.nameRoom(typed);
            }}
          >
            Name it
          </button>
        </p>
      </section>
    </div>
  );
}
