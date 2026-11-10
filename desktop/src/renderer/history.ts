// A message's history ([keys.timeline] history, the TUI's H): every version it had,
// oldest first, and its deletion, as MessageHistory tells them.
import { timestampDate } from "@bufbuild/protobuf/wkt";

import type { Deletion } from "../gen/backend/v1/services_pb";
import type { Message, Revision } from "../gen/backend/v1/types_pb";

/** HistoryView is the history shown: its message, and what MessageHistory said (undefined while it is read). */
export interface HistoryView {
  message: Message;
  revisions: readonly Revision[] | undefined;
  deletion: Deletion | undefined;
  error: string;
}

/** Version is one version as listed: when, what it was (sent, or edited and by how much), and its words. */
export interface Version {
  at: Date | undefined;
  what: string;
  body: string;
}

/**
 * versions lists m's versions. Where none was kept, a message never edited is its own
 * words (a deleted one's, kept under [display.deleted] keep); an edited one has none.
 */
export function versions(m: Message, revisions: readonly Revision[]): Version[] {
  if (revisions.length === 0) {
    return m.edited || m.body === ""
      ? []
      : [{ at: m.timestamp && timestampDate(m.timestamp), what: "sent", body: m.body }];
  }
  return revisions.map((r, i) => ({
    at: r.at && timestampDate(r.at),
    what: i === 0 ? "sent" : `edited${changeNote(revisions[i - 1]?.body ?? "", r.body)}`,
    body: r.body,
  }));
}

/** noVersions says why m lists none, as the TUI's history does. */
export function noVersions(m: Message): string {
  if (m.edited) {
    return "It was edited before its versions were being kept, and the server has nothing left of them.";
  }
  return m.redacted ? "What it said was not kept ([display.deleted] keep is off)." : "It has never been edited.";
}

/** deleted is who deleted m and why, and when, from the history where it says, else from m; undefined while it is there. */
export function deleted(
  m: Message,
  d: Deletion | undefined,
): { by: string; reason: string; at: Date | undefined } | undefined {
  if (!m.redacted) {
    return undefined;
  }
  const by = d?.by || m.redactedBy;
  const reason = d?.by ? d.reason : m.redactedReason;
  const ms = d !== undefined && d.atUnixMs > 0n ? d.atUnixMs : m.redactedAtUnixMs;
  return { by: by === m.sender ? "" : by, reason, at: ms > 0n ? new Date(Number(ms)) : undefined };
}

/** changeNote is how much a version changed, in characters added and removed (the TUI's changeNote). */
export function changeNote(before: string, after: string): string {
  const diff = characters(after) - characters(before);
  if (diff > 0) {
    return ` +${String(diff)}`;
  }
  return diff < 0 ? ` −${String(-diff)}` : " reworded";
}

const graphemes = new Intl.Segmenter();

/** characters is how many characters s reads as: an emoji of several code points is one. */
function characters(s: string): number {
  return Array.from(graphemes.segment(s)).length;
}
