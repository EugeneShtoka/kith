// The filing picker (filing.ts) drawn: the places kithd offers the room, in its order,
// each ticked where it holds the room. Applying asks kithd to file only what changed.
import { useEffect, useRef } from "react";

import { type Filing, filingKey } from "./filing";
import type { Keys } from "./keymap";
import type { Session } from "./session";

export function FilingDialog({ filing, keys, session }: { filing: Filing; keys: Keys; session: Session }) {
  const rows = useRef<HTMLUListElement>(null);
  const places = filing.places;
  const offered = places !== undefined && places.length > 0;
  // The cursor starts on the first place once kithd offers them.
  useEffect(() => {
    if (offered) {
      rows.current?.querySelector("input")?.focus();
    }
  }, [offered]);
  useEffect(() => {
    const key = (e: KeyboardEvent) => {
      const boxes = [...(rows.current?.querySelectorAll("input") ?? [])];
      const at = boxes.findIndex((b) => b === document.activeElement);
      const act = filingKey(e, keys, at, boxes.length);
      if (act === undefined) {
        return;
      }
      // Taken here, the press is no checkbox's own space and no key of the window behind.
      e.preventDefault();
      if (act === "close") {
        session.closeFiling();
      } else if (act === "apply") {
        session.applyFiling();
      } else if (act === "toggle") {
        const p = places?.[at];
        if (p !== undefined) {
          session.tickFiling(p.key);
        }
      } else {
        boxes[act.to]?.focus();
      }
    };
    // Captured: ahead of the window's keys, which would move the room list under the dialog.
    window.addEventListener("keydown", key, true);
    return () => {
      window.removeEventListener("keydown", key, true);
    };
  }, [session, keys, places]);
  return (
    <div className="offer-backdrop">
      <section
        className="offer filing"
        role="dialog"
        aria-modal="true"
        aria-label={`Tags and spaces of ${filing.name}`}
      >
        <h2>
          Tags and spaces{" "}
          <span className="detail filing-room" dir="auto">
            {filing.name}
          </span>
        </h2>
        {places === undefined && filing.note === "" && <p className="detail">Reading the places it can go…</p>}
        {places !== undefined && places.length === 0 && filing.note === "" && (
          <p className="detail">This profile has no space you made and no tag filed by hand.</p>
        )}
        {places !== undefined && places.length > 0 && (
          <ul ref={rows} className="offer-rows filing-rows" aria-busy={filing.busy}>
            {places.map((p) => (
              <li key={p.key}>
                <label>
                  <input
                    type="checkbox"
                    checked={filing.ticked.has(p.key)}
                    disabled={filing.busy}
                    onChange={() => {
                      session.tickFiling(p.key);
                    }}
                  />{" "}
                  <span className="place-name" dir="auto">
                    {p.label}
                  </span>{" "}
                  <span className="detail">{p.space !== "" ? "space" : "tag"}</span>
                </label>
              </li>
            ))}
          </ul>
        )}
        {filing.note !== "" && (
          <p className="problem" role="alert">
            {filing.note}
          </p>
        )}
        <p className="offer-actions">
          <button
            type="button"
            onClick={() => {
              session.closeFiling();
            }}
          >
            Cancel
          </button>
          <button
            type="button"
            className="primary"
            disabled={filing.busy || places === undefined || places.length === 0}
            onClick={() => {
              session.applyFiling();
            }}
          >
            {filing.busy ? "Filing" : "Apply"}
          </button>
        </p>
      </section>
    </div>
  );
}
