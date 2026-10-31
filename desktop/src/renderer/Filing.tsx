// The filing picker (filing.ts) drawn: the places kithd offers the room, in its order,
// each ticked where it holds the room. Applying asks kithd to file only what changed.
import { useEffect } from "react";

import type { Filing } from "./filing";
import type { Session } from "./session";

export function FilingDialog({ filing, session }: { filing: Filing; session: Session }) {
  useEffect(() => {
    const key = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        session.closeFiling();
      }
    };
    window.addEventListener("keydown", key);
    return () => {
      window.removeEventListener("keydown", key);
    };
  }, [session]);
  const places = filing.places;
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
          <ul className="offer-rows filing-rows" aria-busy={filing.busy}>
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
