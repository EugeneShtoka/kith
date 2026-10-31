// The composer's formatting bar: each button puts the Markdown kithd renders
// (internal/markdown: CommonMark, as Element's composer) around what is selected.

export type Markup = "bold" | "italic" | "code" | "link";

/** What each wraps a selection in. */
const around: Record<Exclude<Markup, "link">, string> = { bold: "**", italic: "*", code: "`" };

/**
 * marked is text with [start, end) formatted as markup, and where the caret goes: after
 * the formatted words, or between the marks when nothing was selected, to type there. A
 * link puts the caret where its address goes, or, when the selection is an address,
 * where its words go.
 */
export function marked(text: string, start: number, end: number, markup: Markup): { text: string; caret: number } {
  const before = text.slice(0, start);
  const words = text.slice(start, end);
  const after = text.slice(end);
  if (markup === "link") {
    if (/^[a-z][a-z0-9+.-]*:\S+$/iu.test(words)) {
      return { text: `${before}[](${words})${after}`, caret: start + 1 };
    }
    const link = `[${words}](`;
    return { text: `${before}${link})${after}`, caret: start + (words === "" ? 1 : link.length) };
  }
  const mark = around[markup];
  return {
    text: before + mark + words + mark + after,
    caret: start + mark.length + words.length + (words === "" ? 0 : mark.length),
  };
}
