// The web links in a message, as domain.LinksWithHrefs finds them: what the copy and open
// keys ([keys.timeline] copy_url, open_url) choose from.
import type { Message } from "../gen/backend/v1/types_pb";
import { openable } from "../shared/ipc";

/** What a link key does with the link: copy it, or open it, staying here or going to the browser. */
export type LinkAct = "copy" | "open" | "open_focus";

const linkPattern = /https?:\/\/[^\s<>"'|`]+/g;

/** The closing brackets a link keeps only when it opens as many. */
const openers: Readonly<Record<string, string>> = { ")": "(", "]": "[", "}": "{" };

const count = (s: string, c: string) => s.split(c).length - 1;

/** trimmed is link less the sentence's punctuation after it and closing brackets it does not open. */
function trimmed(link: string): string {
  for (;;) {
    const last = link.at(-1) ?? "";
    const opener = openers[last];
    const loose =
      last !== "" && (".,;:!?'\"".includes(last) || (opener !== undefined && count(link, opener) < count(link, last)));
    if (!loose) {
      return link;
    }
    link = link.slice(0, -1);
  }
}

/** links are m's web links: those in its words in order, then its formatted links' targets, each once. */
export function links(m: Message): string[] {
  const out: string[] = [];
  const add = (link: string) => {
    if (link !== "" && !out.includes(link)) {
      out.push(link);
    }
  };
  for (const found of m.body.matchAll(linkPattern)) {
    add(trimmed(found[0]));
  }
  for (const span of m.format?.spans ?? []) {
    if (span.link && openable(span.href)) {
      add(span.href);
    }
  }
  return out;
}
