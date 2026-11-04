// The help overlay (`help`, `?`): the window's keys as [keys] binds them, filtered as
// typed. Closing puts the focus back where it was.
import { useState } from "react";

import { filtered, type HelpSection, keysOf } from "./help";
import { bound, type Keys } from "./keymap";

export function HelpOverlay({
  keys,
  sections: all,
  close,
}: {
  keys: Keys;
  /** helpSections, and the device's own keys beside them. */
  sections: readonly HelpSection[];
  close: () => void;
}) {
  const [query, setQuery] = useState("");
  // Read as it opens, before the filter takes the focus.
  const [was] = useState(() => document.activeElement);
  const shut = () => {
    close();
    if (was instanceof HTMLElement) {
      was.focus();
    }
  };
  const sections = filtered(all, keys, query);
  return (
    <div className="switcher-backdrop" onClick={shut}>
      <div
        className="switcher help"
        role="dialog"
        aria-modal="true"
        aria-label="Keys"
        onClick={(e) => {
          e.stopPropagation();
        }}
      >
        <input
          autoFocus
          aria-label="Find a key by what it does, or by the key"
          placeholder="Find a key"
          value={query}
          onChange={(e) => {
            setQuery(e.target.value);
          }}
          onKeyDown={(e) => {
            if (bound(keys, "prompt.cancel", e)) {
              e.preventDefault();
              if (query === "") {
                shut();
              } else {
                setQuery("");
              }
            }
          }}
        />
        <div className="help-sections">
          {sections.length === 0 && <p className="detail">Nothing matches</p>}
          {sections.map((s) => (
            <section key={s.title} aria-label={s.title}>
              <h3>{s.title}</h3>
              <dl>
                {s.rows.map((r) => (
                  <div key={r.paths.join(" ")}>
                    <dt>
                      <kbd>{keysOf(r, keys)}</kbd>
                    </dt>
                    <dd>{r.what}</dd>
                  </div>
                ))}
              </dl>
            </section>
          ))}
        </div>
      </div>
    </div>
  );
}
