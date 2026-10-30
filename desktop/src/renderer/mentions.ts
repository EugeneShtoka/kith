// Mentions as the TUI writes them: `@` and part of a name offers the room's people, the
// chosen name goes into the text, and the mention travels with the draft (kithd sends
// those whose name is still in the words: domain.Draft.LiveMentions).
import type { Member } from "../gen/backend/v1/types_pb";

/** How many people the popup offers. */
export const offered = 8;

/**
 * rankMembers orders members for the popup as domain.RankMembers does: recent speakers,
 * then the most mentioned, then the rest as given; at most limit (<= 0: all). Anyone
 * in a rung who is not a member is skipped, and nobody is listed twice.
 */
export function rankMembers(
  all: readonly Member[],
  speakers: readonly string[],
  mentioned: readonly string[],
  limit: number,
): Member[] {
  const byId = new Map(all.map((m) => [m.userId, m]));
  const placed = new Set<string>();
  const out: Member[] = [];
  for (const rung of [speakers, mentioned]) {
    for (const id of rung) {
      const member = byId.get(id);
      if (member !== undefined && !placed.has(id)) {
        placed.add(id);
        out.push(member);
      }
    }
  }
  for (const member of all) {
    if (!placed.has(member.userId)) {
      placed.add(member.userId);
      out.push(member);
    }
  }
  return limit > 0 ? out.slice(0, limit) : out;
}

/** mentionAt is the `@name` being typed before caret: where it starts and what is typed of it. */
export function mentionAt(text: string, caret: number): { start: number; query: string } | undefined {
  const match = /(^|\s)@([^\s@]*)$/.exec(text.slice(0, caret));
  if (match === null) {
    return undefined;
  }
  const query = match[2] ?? "";
  return { start: caret - query.length - 1, query };
}

/** matching is the people whose name or ID holds query, in rank order. */
export function matching(ranked: readonly Member[], query: string): Member[] {
  const q = query.toLowerCase();
  return ranked
    .filter((m) => m.displayName.toLowerCase().includes(q) || m.userId.toLowerCase().includes(q))
    .slice(0, offered);
}

/** nameOf is what a mention of m writes into the text. */
export function nameOf(m: Member): string {
  return m.displayName || m.userId;
}
