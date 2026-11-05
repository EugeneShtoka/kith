// Arrow keys over a row or a grid of buttons (the reactions, the emoji picker): which
// button a key moves to, laid out in rows of columns.

/**
 * arrowTo is the index key (a KeyboardEvent's key) moves to from at, among count
 * buttons in rows of columns: by one across a row and on to the next, by a row up and
 * down (to the last of a shorter last row), Home and End to the ends. -1 is above the
 * first row, where the picker's search field is; undefined is a key that moves nothing.
 */
export function arrowTo(key: string, at: number, count: number, columns: number): number | undefined {
  const cols = Math.max(1, columns);
  switch (key) {
    case "ArrowRight":
      return Math.min(at + 1, count - 1);
    case "ArrowLeft":
      return Math.max(at - 1, 0);
    case "ArrowDown":
      return at + cols < count ? at + cols : Math.floor(at / cols) < Math.floor((count - 1) / cols) ? count - 1 : at;
    case "ArrowUp":
      return at - cols;
    case "Home":
      return 0;
    case "End":
      return count - 1;
  }
  return undefined;
}

/** columnsOf is how many of buttons the first row holds: those as high as the first. */
export function columnsOf(buttons: readonly HTMLElement[]): number {
  const top = buttons[0]?.offsetTop;
  const i = buttons.findIndex((b) => b.offsetTop !== top);
  return i < 0 ? buttons.length : i;
}
