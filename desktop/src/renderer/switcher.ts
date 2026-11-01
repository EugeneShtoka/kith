// The switcher (ctrl+K), as the TUI's: type part of a room's, a person's or a place's
// name and go there. Rooms with something unread come first, then the rest, then the
// rail's places; a DM is found by the person's name, as its room carries them. A field
// term (role:attorney) lists, after them, the people whose card meets it (kithd's People).
import type { PersonListed, RailRow } from "../gen/backend/v1/services_pb";
import type { Room, Unread } from "../gen/backend/v1/types_pb";
import { unreadOf } from "./rooms";

/** One row of the switcher: a room to open, or the rail row to go to. */
export interface Choice {
  key: string;
  label: string;
  detail: string;
  /** What typing is matched against. */
  match: string;
  go: { room: string } | { row: number } | { person: string };
}

/** choices is everything the switcher reaches, in the order it offers them. */
export function choices(
  rooms: readonly Room[],
  unread: ReadonlyMap<string, Unread>,
  rail: readonly RailRow[],
): Choice[] {
  const room = (r: Room): Choice => {
    const count = unreadOf(unread.get(r.id)).count;
    const detail = [count > 0 ? `${String(count)} unread` : "", r.isDirect ? "DM" : ""].filter((d) => d !== "");
    return {
      key: `room ${r.id}`,
      label: r.name || r.id,
      detail: detail.join(" · "),
      // The members and the ID match too, though not shown: a DM by the person's name,
      // and rooms of one name told apart.
      match: [r.name, ...r.members, r.id].join(" "),
      go: { room: r.id },
    };
  };
  const unreadFirst = [
    ...rooms.filter((r) => unreadOf(unread.get(r.id)).count > 0),
    ...rooms.filter((r) => unreadOf(unread.get(r.id)).count === 0),
  ];
  return [
    ...unreadFirst.map(room),
    ...rail.map((row, i) => ({
      key: `row ${row.key} ${row.space}`,
      label: row.label,
      detail: "place",
      match: `${row.label} ${row.key}`,
      go: { row: i },
    })),
  ];
}

/**
 * hasFieldTerm is whether typed holds a field term, key:value, as phonebook.ParseQuery
 * reads one: a word with something on both sides of its first colon.
 */
export function hasFieldTerm(typed: string): boolean {
  return typed.split(/\s+/).some((w) => {
    const at = w.indexOf(":");
    return at > 0 && at < w.length - 1;
  });
}

/** personChoices is a row for each card People listed, by its name, to go to the person. */
export function personChoices(people: readonly PersonListed[]): Choice[] {
  return people.map((p) => ({
    key: `person ${p.card}`,
    label: p.card,
    detail: [...p.matched.map((m) => `${m.key}: ${m.values.join(", ")}`), "person"].join(" · "),
    match: "",
    go: { person: p.card },
  }));
}

/** matching is the choices whose text has every word typed, in any case. */
export function matching(all: readonly Choice[], typed: string): Choice[] {
  const words = typed
    .toLowerCase()
    .split(/\s+/)
    .filter((w) => w !== "");
  return all.filter((c) => {
    const text = `${c.label} ${c.match}`.toLowerCase();
    return words.every((w) => text.includes(w));
  });
}
