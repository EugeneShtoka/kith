// Back and forward over the rooms opened, as the TUI's jumplist: only deliberate opens (a
// room picked, the switcher, a search hit) are recorded. Going back puts where the window
// was on the forward side; opening somewhere new drops that side.

/** How many places each side keeps. */
const depth = 32;

/** Where the window has been: back's last is the place left last, forward's the place returned from last. */
export interface Jumplist {
  at: string;
  back: readonly string[];
  forward: readonly string[];
}

export const nowhere: Jumplist = { at: "", back: [], forward: [] };

/** arrived records a deliberate open of room; opening where the window is records nothing. */
export function arrived(j: Jumplist, room: string): Jumplist {
  if (room === "" || room === j.at) {
    return j;
  }
  return j.at === "" ? { ...j, at: room } : { at: room, back: push(j.back, j.at), forward: [] };
}

/**
 * step goes back (or forward) to the last place on that side that still exists, the
 * places on the way that do not forgotten on both sides; to is "" when none is left.
 */
export function step(j: Jumplist, back: boolean, exists: (room: string) => boolean): { j: Jumplist; to: string } {
  let from = back ? j.back : j.forward;
  let onto = back ? j.forward : j.back;
  for (let to = from.at(-1); to !== undefined; to = from.at(-1)) {
    from = from.slice(0, -1);
    if (!exists(to)) {
      from = from.filter((r) => r !== to);
      onto = onto.filter((r) => r !== to);
      continue;
    }
    if (j.at !== "") {
      onto = push(onto, j.at);
    }
    return { j: back ? { at: to, back: from, forward: onto } : { at: to, back: onto, forward: from }, to };
  }
  return { j: back ? { ...j, back: from, forward: onto } : { ...j, back: onto, forward: from }, to: "" };
}

/** push appends room, keeping the newest depth and never repeating the last. */
function push(stack: readonly string[], room: string): readonly string[] {
  return stack.at(-1) === room ? stack : [...stack, room].slice(-depth);
}
