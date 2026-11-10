// A message's history over the window: each version it had, its deletion, and its
// attachment, the only place a deleted message's is shown (the TUI's H).
import { deleted, type HistoryView, noVersions, versions } from "./history";
import { bound, type Keys } from "./keymap";
import { attachmentURL } from "./media";
import type { Session } from "./session";

const clock = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "medium" });

export function HistoryDialog({ history, keys, session }: { history: HistoryView; keys: Keys; session: Session }) {
  const m = history.message;
  const listed = history.revisions && versions(m, history.revisions);
  const gone = deleted(m, history.deletion);
  const who = m.senderName || m.sender;
  const title =
    listed === undefined && history.error === ""
      ? `${who} · reading the history`
      : listed !== undefined && listed.length > 1
        ? `${who} · ${String(listed.length)} versions`
        : `${who} · message history`;
  const media = m.media;
  return (
    <div className="offer-backdrop">
      <section
        className="offer history"
        role="dialog"
        aria-modal="true"
        aria-label={title}
        onKeyDown={(e) => {
          if (bound(keys, "picker.close", e)) {
            e.preventDefault();
            session.closeHistory();
          }
        }}
      >
        <h2 dir="auto">{title}</h2>
        {history.error !== "" && <p className="detail">Could not read the history: {history.error}</p>}
        {listed?.length === 0 && <p className="detail">{noVersions(m)}</p>}
        {listed !== undefined && listed.length > 0 && (
          <ol className="versions">
            {listed.map((v, i) => (
              <li key={i}>
                <p className="detail">
                  {v.at && <time>{clock.format(v.at)}</time>} · {v.what}
                </p>
                <p className="body" dir="auto">
                  {v.body}
                </p>
              </li>
            ))}
          </ol>
        )}
        {gone && (
          <p className="deleted">
            Deleted{gone.by && <> by {gone.by}</>}
            {gone.reason && <>: {gone.reason}</>}
            {gone.at && <> · {clock.format(gone.at)}</>}
          </p>
        )}
        {media && (
          <div className="history-attachment">
            {media.type === "image" && <img className="picture" src={attachmentURL(m)} alt={media.name} />}
            {media.type === "audio" && <audio className="player" controls preload="metadata" src={attachmentURL(m)} />}
            {media.type === "video" && <video className="player" controls preload="metadata" src={attachmentURL(m)} />}
            <p className="detail" dir="auto">
              📎 {media.name}
            </p>
          </div>
        )}
        <p className="offer-actions">
          {media && (
            <button type="button" onClick={() => void session.save(m)}>
              Save the attachment
            </button>
          )}
          <button
            type="button"
            className="primary"
            autoFocus
            onClick={() => {
              session.closeHistory();
            }}
          >
            Close
          </button>
        </p>
      </section>
    </div>
  );
}
