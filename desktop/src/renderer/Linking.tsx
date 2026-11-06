// Which of a message's links a link key ([keys.timeline] copy_url, open_url,
// open_url_focus) takes, where it holds several, as the TUI's chooser asks.
import { bound, type Keys } from "./keymap";
import type { LinkAct } from "./links";
import type { Session } from "./session";

const titles: Readonly<Record<LinkAct, string>> = {
  copy: "Copy which link?",
  open: "Open which link?",
  open_focus: "Go to which link?",
};

export function LinkingDialog({
  linking,
  keys,
  session,
}: {
  linking: { act: LinkAct; links: readonly string[] };
  keys: Keys;
  session: Session;
}) {
  return (
    <div className="offer-backdrop">
      <section
        className="offer"
        role="dialog"
        aria-modal="true"
        aria-label={titles[linking.act]}
        onKeyDown={(e) => {
          const rows = [...e.currentTarget.querySelectorAll<HTMLElement>("li button")];
          const at = rows.indexOf(document.activeElement as HTMLElement);
          const to = bound(keys, "nav.down", e) ? at + 1 : bound(keys, "nav.up", e) ? at - 1 : undefined;
          if (to !== undefined) {
            e.preventDefault();
            rows[Math.min(Math.max(to, 0), rows.length - 1)]?.focus();
          } else if (bound(keys, "picker.close", e)) {
            e.preventDefault();
            session.closeLinking();
          }
        }}
      >
        <h2>{titles[linking.act]}</h2>
        <ul className="link-choices">
          {linking.links.map((link, i) => (
            <li key={link}>
              <button
                type="button"
                autoFocus={i === 0}
                onClick={() => {
                  session.pickLink(i);
                }}
              >
                <span dir="auto">{link}</span>
              </button>
            </li>
          ))}
        </ul>
        <p className="offer-actions">
          <button
            type="button"
            onClick={() => {
              session.closeLinking();
            }}
          >
            Cancel
          </button>
        </p>
      </section>
    </div>
  );
}
