// The pane keys (focus_rail … focus_composer) reach into the left column the ☰ button folds:
// a pane they go to is shown first, as the button would show it.

/** Folded is what a wide window folds away: nothing, the rail, or the whole left column. */
export type Folded = "none" | "places" | "rooms";

/** A pane a pane key goes to. */
export type Target = "rail" | "rooms" | "timeline" | "composer";

/** Width is the window's (styles.css): wide, narrow (under 960px) or narrowest (under 640px). */
export type Width = "wide" | "narrow" | "narrowest";

/** Layout is how the left column is laid out: folded (a wide window), unfolded (a narrow one's toggle). */
export interface Layout {
  folded: Folded;
  unfolded: boolean;
}

/**
 * reveal is the layout that shows target. A wide window unfolds the rail for the rail, and the
 * room list (the rail staying as it was) for the room list. A narrow one lays out the rail it
 * folds for the rail; the narrowest, which folds the whole left column over the room, lays it
 * out for the rail or the room list, and puts it away for the room, as picking a room does.
 */
export function reveal(target: Target, now: Layout, width: Width): Layout {
  const left = target === "rail" || target === "rooms";
  switch (width) {
    case "narrowest":
      return { ...now, unfolded: left };
    case "narrow":
      return target === "rail" ? { ...now, unfolded: true } : now;
    case "wide":
      if (target === "rail") {
        return { ...now, folded: "none" };
      }
      return target === "rooms" && now.folded === "rooms" ? { ...now, folded: "places" } : now;
  }
}
