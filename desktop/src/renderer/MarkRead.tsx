// A place's unread rooms marked read (rail.mark_read, as the TUI's): asked first, since it
// reads what nobody opened.
import { bound, type Keys } from "./keymap";
import { roomsPhrase, type Session } from "./session";

export function MarkReadDialog({
  marking,
  keys,
  session,
}: {
  marking: { label: string; rooms: readonly string[] };
  keys: Keys;
  session: Session;
}) {
  const ask = `Mark ${roomsPhrase(marking.rooms.length)} in ${marking.label} read?`;
  return (
    <div className="offer-backdrop">
      <section
        className="offer"
        role="dialog"
        aria-modal="true"
        aria-label={ask}
        onKeyDown={(e) => {
          if (bound(keys, "prompt.cancel", e)) {
            e.preventDefault();
            session.closeMarking();
          }
        }}
      >
        <h2 dir="auto">{ask}</h2>
        <p className="offer-actions">
          <button
            type="button"
            onClick={() => {
              session.closeMarking();
            }}
          >
            Cancel
          </button>
          <button
            type="button"
            className="primary"
            autoFocus
            onClick={() => {
              session.markAsked();
            }}
          >
            Mark read
          </button>
        </p>
      </section>
    </div>
  );
}
