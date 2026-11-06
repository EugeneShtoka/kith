// The links in a message, as domain.MessageLinks finds them: what the copy and open keys
// ([keys.timeline] copy_url, open_url) choose from.
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

/**
 * The schemes of the chat links kith follows that are no web links (matrix:, Telegram's,
 * WhatsApp's, Signal's).
 * shortcut: by scheme alone, where Go also parses each (and takes a bare t.me/name); kithd's Follow judges the link
 */
const chatSchemes = /^(matrix|tg|whatsapp|sgnl):\S/i;

/** The words a chat link may be, split at white space (domain's isURIBoundary). */
const words = /\s+/;

/**
 * links are m's links: the web links in its words in order, then its formatted links' web
 * targets, then the chat links that are no web links, from its words and then its
 * formatted links, each once.
 */
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
  const hrefs = (m.format?.spans ?? []).filter((s) => s.link).map((s) => s.href);
  for (const href of hrefs.filter(openable)) {
    add(href);
  }
  for (const word of m.body.split(words)) {
    const candidate = word.replace(/^[.,;:!?"'()[\]<>]+|[.,;:!?"'()[\]<>]+$/g, "");
    if (chatSchemes.test(candidate)) {
      add(candidate);
    }
  }
  for (const href of hrefs.filter((h) => chatSchemes.test(h))) {
    add(href);
  }
  return out;
}
