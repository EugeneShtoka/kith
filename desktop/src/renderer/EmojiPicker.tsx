// The emoji picker: every emoji the set offers, in the room's order, narrowed by name as
// the person types; one picked is toned ([display] skin_tone).
import { useState } from "react";

import type { ComposerView } from "./compose";
import { type EmojiKind, ranked, shortcodeOf, toned } from "./emoji";
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
  const q = query.trim().toLowerCase().replaceAll(":", "");
  const all = ranked(emoji.set.all, kind, emoji.scores, emoji.statics);
  const named = new Set(emoji.set.names.filter((name) => name.includes(q)).map((name) => emoji.set.byName.get(name)));
  const found = q === "" ? all : all.filter((e) => named.has(e));
  return (
    <div className="emoji-picker" role="dialog" aria-label="Emoji">
      <input
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
          }
        }}
      />
      <div className="emoji-grid">
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
