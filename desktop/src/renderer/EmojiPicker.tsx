// The emoji picker: every emoji the set offers, in the room's order, narrowed by name as
// the person types; one picked is toned ([display] skin_tone).
import { useRef, useState } from "react";

import type { ComposerView } from "./compose";
import { type EmojiKind, ranked, shortcodeOf, toned } from "./emoji";
import { arrowTo, columnsOf } from "./gridnav";
import { bound, type Keys } from "./keymap";

/** How many emoji the grid shows at once: typing narrows the rest. */
const shown = 400;

export function EmojiPicker({
  emoji,
  keys,
  kind,
  onPick,
  onClose,
}: {
  emoji: ComposerView["emoji"];
  keys: Keys;
  kind: EmojiKind;
  onPick: (emoji: string) => void;
  onClose: () => void;
}) {
  const [query, setQuery] = useState("");
  const field = useRef<HTMLInputElement>(null);
  const q = query.trim().toLowerCase().replaceAll(":", "");
  const all = ranked(emoji.set.all, kind, emoji.scores, emoji.statics);
  const named = new Set(emoji.set.names.filter((name) => name.includes(q)).map((name) => emoji.set.byName.get(name)));
  const found = q === "" ? all : all.filter((e) => named.has(e));
  return (
    <div className="emoji-picker" role="dialog" aria-label="Emoji">
      <input
        ref={field}
        autoFocus
        aria-label="Find an emoji by name"
        placeholder="Find an emoji by name"
        value={query}
        onChange={(e) => {
          setQuery(e.target.value);
        }}
        onKeyDown={(e) => {
          if (bound(keys, "emoji.cancel", e)) {
            e.preventDefault();
            onClose();
          } else if (bound(keys, "emoji.accept", e) && found[0] !== undefined) {
            e.preventDefault();
            onPick(toned(found[0], emoji.tone));
          } else if (e.key === "ArrowDown") {
            e.preventDefault(); // into the grid, at its first
            e.currentTarget.parentElement?.querySelector<HTMLElement>(".emoji-grid button")?.focus();
          }
        }}
      />
      <div
        className="emoji-grid"
        role="group"
        aria-label="Emoji"
        onKeyDown={(e) => {
          const buttons = [...e.currentTarget.querySelectorAll<HTMLElement>("button")];
          const at = buttons.indexOf(document.activeElement as HTMLElement);
          const to = at < 0 ? undefined : arrowTo(e.key, at, buttons.length, columnsOf(buttons));
          if (bound(keys, "emoji.cancel", e)) {
            e.preventDefault();
            onClose();
          } else if (to !== undefined) {
            e.preventDefault();
            (to < 0 ? field.current : buttons[to])?.focus(); // above the first row: the search field
          }
        }}
      >
        {found.slice(0, shown).map((e) => (
          <button
            key={e}
            type="button"
            title={`:${shortcodeOf(emoji.set, e)}:`}
            onClick={() => {
              onPick(toned(e, emoji.tone));
            }}
          >
            {toned(e, emoji.tone)}
          </button>
        ))}
      </div>
    </div>
  );
}
