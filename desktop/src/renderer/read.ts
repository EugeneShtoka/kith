// Whether a read receipt is announced to the people in a room or kept private: send_receipts
// and [[display.read_rule]] (domain.ResolveRead), held to Go by the conformance table
// internal/domain/testdata/read.json; and how long the list's cursor rests on a room
// before it is read (read_delay).
import type { DeviceSettings, ReadRule } from "../gen/device/v1/device_pb";

// The rungs a rule names a place on (domain.Rank), widest first; the narrowest wins.
const rankSpace = 1;
const rankSender = 2;
const rankRoom = 3;
const rankSpaceSender = 5;
const rankRoomSender = 6;

/** isHomeEntry: entry names rail rows (space:, tag:, place:) and a name (domain.ParseHomeEntry). */
function isHomeEntry(entry: string): boolean {
  const e = entry.trim().toLowerCase();
  return ["space:", "tag:", "place:"].some((p) => e.startsWith(p) && e.slice(p.length).trim() !== "");
}

/**
 * rank is how narrowly rule names a receipt for room's message by sender (0: not at all),
 * and whether that depends on the room's homes, which the window is not told.
 */
function rank(rule: ReadRule, room: string, sender: string): { rank: number; maybe: boolean } {
  if (rule.sender !== "" && rule.sender !== sender) {
    return { rank: 0, maybe: false };
  }
  const named = rule.sender !== "";
  const place = rule.match.trim();
  if (rule.match === "") {
    return { rank: named ? rankSender : 0, maybe: false };
  }
  if (room !== "" && place === room) {
    return { rank: named ? rankRoomSender : rankRoom, maybe: false };
  }
  if (isHomeEntry(place)) {
    return { rank: named ? rankSpaceSender : rankSpace, maybe: true };
  }
  return { rank: 0, maybe: false };
}

/**
 * outcomes are what each rule that could decide room's message by sender says by say,
 * the defaults' where none does: the rule winning outright, and each rule naming a home
 * that would beat it did the room turn out to be in that home (the window does not know
 * its homes).
 */
function outcomes<T>(
  rules: readonly ReadRule[],
  room: string,
  sender: string,
  say: (rule: ReadRule | undefined) => T,
): T[] {
  const ranked = rules.map((rule) => ({ rule, ...rank(rule, room, sender) }));
  // The winner where no home rule matches: the narrowest certain one, the later on a tie.
  let best: (typeof ranked)[number] | undefined;
  for (const r of ranked) {
    if (!r.maybe && r.rank > 0 && r.rank >= (best?.rank ?? 0)) {
      best = r;
    }
  }
  const out = [say(best?.rule)];
  ranked.forEach((r, i) => {
    const beats = best === undefined || r.rank > best.rank || (r.rank === best.rank && i > ranked.indexOf(best));
    if (r.maybe && beats) {
      out.push(say(r.rule));
    }
  });
  return out;
}

/**
 * announces says whether a receipt in room for sender's message is announced: only when
 * every rule that could decide it says so, a rule naming a home included. No settings
 * (none read yet, or the read failed) is private: a receipt never tells people what
 * the settings might forbid.
 */
export function announces(settings: DeviceSettings | undefined, room: string, sender: string): boolean {
  if (settings === undefined) {
    return false;
  }
  return outcomes(settings.readRules, room, sender, (rule) => rule?.send ?? settings.sendReceipts).every(Boolean);
}

/**
 * readAfter is the seconds the list's cursor rests on room, its newest message sender's,
 * before it is read without being entered ([display] read_delay and the rules' delay);
 * negative is never. Where rules disagree, the longest wait, and never over any; no
 * settings is never.
 */
export function readAfter(settings: DeviceSettings | undefined, room: string, sender: string): number {
  if (settings === undefined) {
    return -1;
  }
  const delays = outcomes(settings.readRules, room, sender, (rule) => Number(rule?.delay ?? settings.readDelay));
  return delays.some((d) => d < 0) ? -1 : Math.max(...delays);
}
