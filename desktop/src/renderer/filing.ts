// Filing a room into tags and spaces: the places kithd offers it (FilingPlaces: the
// TUI's order and ticks), the ticks changed in the window, and what a filing asks of
// kithd and what came of it. kithd files it (FileRoom): it judges the tags on the config
// as it is when it writes, archives on the network and records releases, so the window
// holds no config and builds no rail; the rail moves when kithd says it changed.
import type { MessageInitShape } from "@bufbuild/protobuf";

import {
  type FileRoomResponse,
  type FilingPlace,
  SpaceFiledOutcome,
  type SpaceFilingSchema,
  type TagFilingSchema,
} from "../gen/backend/v1/services_pb";

type TagAsked = MessageInitShape<typeof TagFilingSchema>;
type SpaceAsked = MessageInitShape<typeof SpaceFilingSchema>;

/** A place offered: a space (its ID) or a tag (its name), keyed apart, and whether it holds the room. */
export interface Place {
  key: string;
  space: string;
  tag: string;
  label: string;
  in: boolean;
}

/** Filing is the picker open on one room. */
export interface Filing {
  room: string;
  name: string;
  /** The places as kithd offered them, in its order; undefined while asked. */
  places: readonly Place[] | undefined;
  ticked: ReadonlySet<string>;
  /** The filing is with kithd: the picker takes no other. */
  busy: boolean;
  /** Why there is nothing to tick: the places could not be read, or none matched. */
  note: string;
}

/** placeKey keys a place: tags and spaces never share one. */
function placeKey(p: { space: string; tag: string }): string {
  return p.space !== "" ? `space ${p.space}` : `tag ${p.tag}`;
}

/** opened is the picker on room, its places asked for. */
export function opened(room: string, name: string): Filing {
  return { room, name, places: undefined, ticked: new Set(), busy: false, note: "" };
}

/** offered is f with kithd's places, ticked where they hold the room; unmatched: [display] filing_spaces names none. */
export function offered(f: Filing, places: readonly FilingPlace[], unmatched: boolean): Filing {
  const shown = places.map((p) => ({ key: placeKey(p), space: p.space, tag: p.tag, label: p.label, in: p.in }));
  return {
    ...f,
    places: shown,
    ticked: new Set(shown.filter((p) => p.in).map((p) => p.key)),
    note: unmatched ? "Nothing in [display] filing_spaces matches a space of this profile." : "",
  };
}

/** toggled is f with key ticked or not; a busy picker changes nothing. */
export function toggled(f: Filing, key: string): Filing {
  if (f.busy || !(f.places ?? []).some((p) => p.key === key)) {
    return f;
  }
  const ticked = new Set(f.ticked);
  if (!ticked.delete(key)) {
    ticked.add(key);
  }
  return { ...f, ticked };
}

/** Asked is what a filing asks of kithd: the places whose tick differs from what they hold. */
export interface Asked {
  tags: TagAsked[];
  spaces: SpaceAsked[];
}

/**
 * asked is the filing f's ticks ask for: only the places ticked otherwise than they hold
 * the room, so a place nobody touched is never written.
 */
export function asked(f: Filing): Asked {
  const out: Asked = { tags: [], spaces: [] };
  for (const p of f.places ?? []) {
    const ticked = f.ticked.has(p.key);
    if (ticked === p.in) {
      continue;
    }
    if (p.space !== "") {
      out.spaces.push({ spaceId: p.space, in: ticked });
    } else {
      out.tags.push({ tag: p.tag, in: ticked });
    }
  }
  return out;
}

/** told is the one line a filing comes to, as the TUI says it: where the room went, and what was refused. */
export function told(f: Filing, ask: Asked, r: FileRoomResponse): string {
  const label = (key: string) => f.places?.find((p) => p.key === key)?.label ?? key;
  const tags = (into: boolean) =>
    ask.tags.filter((t) => t.in === into).map((t) => label(placeKey({ space: "", tag: t.tag ?? "" })));
  const parts: string[] = [];
  const into = tags(true);
  const outOf = tags(false);
  const moved: string[] = [];
  if (into.length > 0) {
    moved.push("in " + into.join(", "));
  }
  if (outOf.length > 0) {
    moved.push("out of " + outOf.join(", "));
  }
  if (moved.length > 0) {
    parts.push(`${f.name} is ${moved.join(", and ")}`);
  }
  parts.push(...spacesTold(r.spaces, (id) => label(placeKey({ space: id, tag: "" })), ask.spaces));
  if (r.archiveFailed !== "") {
    parts.push(`its network did not archive or unarchive it: ${r.archiveFailed}`);
  }
  if (r.releaseFailed !== "") {
    parts.push(`out of a filter's tag here, but a filter may move it back elsewhere: ${r.releaseFailed}`);
  }
  return parts.length > 0 ? parts.join(" · ") : "No change.";
}

/** spacesTold says what came of each space filing, grouped as the TUI's line groups them. */
function spacesTold(
  filed: FileRoomResponse["spaces"],
  label: (id: string) => string,
  asked: readonly SpaceAsked[],
): string[] {
  const added: string[] = [];
  const removed: string[] = [];
  const kept: string[] = [];
  const refused: string[] = [];
  for (const s of filed) {
    const name = label(s.spaceId);
    const into = asked.find((a) => a.spaceId === s.spaceId)?.in ?? false;
    switch (s.outcome) {
      case SpaceFiledOutcome.FILED:
        (into ? added : removed).push(name);
        break;
      case SpaceFiledOutcome.PARENT_KEPT:
        kept.push(name);
        break;
      default:
        refused.push(s.error !== "" ? `${name} (${s.error})` : name);
    }
  }
  const out: string[] = [];
  if (added.length > 0) {
    out.push("filed into " + added.join(", "));
  }
  if (removed.length > 0) {
    out.push("taken out of " + removed.join(", "));
  }
  if (kept.length > 0) {
    out.push(`moved in ${kept.join(", ")} but kept its own record of where it lives (no power there)`);
  }
  if (refused.length > 0) {
    out.push("refused by " + refused.join(", "));
  }
  return out;
}
