// The one-time offers (offers.ts) drawn: a dictionary, word counts or the completion
// model to install, each row ticked as kithd suggests. Unticked and accepted, a row is
// turned down for good; "Not now" puts the offers away until the window starts again.
import { useState } from "react";

import type { BackupWarning } from "../gen/backend/v1/services_pb";
import type { Prompt } from "./offers";
import type { Session } from "./session";

export function OfferDialog({ prompt, session }: { prompt: Prompt; session: Session }) {
  const [ticked, setTicked] = useState(() => new Set(prompt.rows.filter((r) => r.ticked).map((r) => r.tag)));
  return (
    <div className="offer-backdrop">
      <section className="offer" role="dialog" aria-modal="true" aria-label={prompt.title}>
        <h2>{prompt.title}</h2>
        <ul className="offer-rows">
          {prompt.rows.map((r) => (
            <li key={r.tag}>
              <label>
                <input
                  type="checkbox"
                  checked={ticked.has(r.tag)}
                  onChange={(e) => {
                    const next = new Set(ticked);
                    if (e.target.checked) {
                      next.add(r.tag);
                    } else {
                      next.delete(r.tag);
                    }
                    setTicked(next);
                  }}
                />{" "}
                <strong>{r.tag}</strong> <span className="detail">{r.detail}</span>
              </label>
            </li>
          ))}
        </ul>
        <p className="detail">Unticked, a row is not offered again (device.toml).</p>
        <p className="offer-actions">
          <button
            type="button"
            onClick={() => {
              session.dismissOffer();
            }}
          >
            Not now
          </button>
          <button
            type="button"
            className="primary"
            autoFocus
            onClick={() => {
              session.acceptOffer([...ticked]);
            }}
          >
            {ticked.size > 0 ? "Install" : "Done"}
          </button>
        </p>
      </section>
    </div>
  );
}

/**
 * kithd's warning about a backups folder that does not keep them (one under /tmp):
 * "Don't warn again" has kithd keep that folder dismissed; Close shows it again when the
 * window starts again.
 */
export function BackupWarningDialog({ warning, session }: { warning: BackupWarning; session: Session }) {
  return (
    <div className="offer-backdrop">
      <section className="offer" role="dialog" aria-modal="true" aria-label="Backups folder">
        <h2>Backups in this folder do not last</h2>
        <p>{warning.why}</p>
        <p className="offer-actions">
          <button
            type="button"
            onClick={() => {
              session.closeBackupWarning(true);
            }}
          >
            Don't warn again
          </button>
          <button
            type="button"
            className="primary"
            autoFocus
            onClick={() => {
              session.closeBackupWarning(false);
            }}
          >
            Close
          </button>
        </p>
      </section>
    </div>
  );
}
