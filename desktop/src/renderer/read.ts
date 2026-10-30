// Whether a read receipt is announced to the people in a room or kept private: send_receipts
// and [[display.read_rule]] (domain.ResolveRead), held to Go by the conformance table
// internal/domain/testdata/read.json.
import type { DeviceSettingsResponse, ReadRule } from "../gen/device/v1/device_pb";

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
 * announces says whether a receipt in room for sender's message is announced: only when
 * every rule that could decide it says so. A rule naming a home might or might not name
 * this room (the window does not know its homes), so where one could win, the receipt
 * is announced only if it and the rule winning without it both say announce. No settings
 * (none read yet, or the read failed) is private: a receipt never tells people what
 * the settings might forbid.
 */
export function announces(settings: DeviceSettingsResponse | undefined, room: string, sender: string): boolean {
  if (settings === undefined) {
    return false;
  }
  const send = (rule: ReadRule | undefined) => rule?.send ?? settings.sendReceipts;
  const ranked = settings.readRules.map((rule) => ({ rule, ...rank(rule, room, sender) }));
  // The winner where no home rule matches: the narrowest certain one, the later on a tie.
  let best: (typeof ranked)[number] | undefined;
  for (const r of ranked) {
    if (!r.maybe && r.rank > 0 && r.rank >= (best?.rank ?? 0)) {
      best = r;
    }
  }
  const outcomes = [send(best?.rule)];
  // Each home rule that would beat it, did the room turn out to be in that home.
  ranked.forEach((r, i) => {
    const beats = best === undefined || r.rank > best.rank || (r.rank === best.rank && i > ranked.indexOf(best));
    if (r.maybe && beats) {
      outcomes.push(send(r.rule));
    }
  });
  return outcomes.every(Boolean);
}
