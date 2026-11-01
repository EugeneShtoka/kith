// People and their phonebook cards, served by kithd from its own [phonebook] dir (the
// window has no folder of its own): the People list, as the TUI's :people; a person's
// card, as its I; a field edited, cleared or added. A field is written over the card as
// the window read it (SetPersonField at its revision). Told the card changed since
// (ABORTED: a notes app, the TUI or another window wrote it), the window reads it again
// and writes its key onto it, unless the key no longer holds what the window showed or
// the card is another now: then it keeps what is there and says so, as the settings
// page keeps another writer's setting.
import type { PeopleResponse, PersonCardResponse, PersonListed, PhonebookCard } from "../gen/backend/v1/services_pb";
import { said } from "./devices";
import { aborted } from "./settings";

/** Whom the window names to kithd: a person by an ID, a card People listed, or a DM's one person. */
export type Who = { person: string } | { card: string } | { roomId: string };

/** What People reaches of kithd, through main. */
export interface PeopleCalls {
  list(query: string): Promise<PeopleResponse>;
  card(who: Who): Promise<PersonCardResponse>;
  /** set writes key = values over the card at revision ("" none yet); name names a new card. */
  set(who: Who, name: string, revision: string, key: string, values: string[], list: boolean): Promise<PhonebookCard>;
  keys(): Promise<readonly string[]>;
}

/** A field being written: its key, the text typed (commas make a list), and whether the card holds it. */
export interface Editing {
  key: string;
  text: string;
  list: boolean;
  fresh: boolean;
}

/** Add a field: the keys the folder's cards use that this one does not, then a new one named. */
export interface Adding {
  /** undefined while read. */
  keys: readonly string[] | undefined;
  /** A new key being named, and its name so far. */
  naming: boolean;
  name: string;
}

/** A person's card, open. */
export interface CardView {
  who: Who;
  /** What the window calls them. */
  name: string;
  /** The card as kithd last answered it; undefined while read. */
  read: PersonCardResponse | undefined;
  adding: Adding | undefined;
  editing: Editing | undefined;
  /** A field is with kithd: the card takes no other. */
  busy: boolean;
  /** What the card says: a write that gave way to another writer's, or a failure. */
  note: string;
}

/** The People list, narrowed by what is typed. */
export interface ListView {
  query: string;
  /** undefined while read. */
  people: readonly PersonListed[] | undefined;
  /** The cards kithd cannot read, by name, and why. */
  unreadable: readonly { card: string; reason: string }[];
  note: string;
}

export interface PeopleView {
  list: ListView | undefined;
  card: CardView | undefined;
}

export const noPeople: PeopleView = { list: undefined, card: undefined };

/** How often one field is written, each try lost to another writer, before the card gives up. */
export const writeTries = 4;

/** valuesOf is key's values on card, its identifiers and fields alike; none when it holds no such key. */
export function valuesOf(card: PhonebookCard | undefined, key: string): string[] {
  return [...(card?.ids ?? []), ...(card?.fields ?? [])].find((f) => f.key === key)?.values ?? [];
}

/** holds is whether card has key at all, and whether it writes it as a list. */
function holds(card: PhonebookCard | undefined, key: string): { has: boolean; list: boolean } {
  const f = [...(card?.ids ?? []), ...(card?.fields ?? [])].find((x) => x.key === key);
  return { has: f !== undefined, list: f?.list ?? false };
}

/** valuesTyped is a field's text as values: commas between them, blanks dropped (the TUI's). */
export function valuesTyped(text: string): string[] {
  return text
    .split(",")
    .map((v) => v.trim())
    .filter((v) => v !== "");
}

/** sameValues is whether two lists of values say the same, in order. */
function sameValues(a: readonly string[], b: readonly string[]): boolean {
  return a.length === b.length && a.every((v, i) => v === b[i]);
}

/** Written is a field's write done: the card as it is now, and whether another writer's change was kept instead. */
export interface Written {
  read: PersonCardResponse;
  kept: boolean;
}

/**
 * writeField writes key = values over the card as shown. Told it changed since, it reads
 * it again: on the same card (or still none) with key holding what was shown, the
 * write is made onto it, at most writeTries times; on another card (renamed, a second
 * one theirs, gone) or with key changed, what is there is kept. A failure other than
 * ABORTED, or the last try's, is thrown.
 */
export async function writeField(
  calls: PeopleCalls,
  who: Who,
  name: string,
  shown: PersonCardResponse,
  key: string,
  values: string[],
  list: boolean,
): Promise<Written> {
  let read = shown;
  for (let tries = 1; ; tries++) {
    try {
      const card = await calls.set(who, name, read.card?.revision ?? "", key, values, list);
      return { read: { ...read, card }, kept: false };
    } catch (err) {
      if (!aborted(err) || tries >= writeTries) {
        throw err;
      }
    }
    read = await calls.card(who);
    const was = shown.card?.card ?? "";
    if ((was !== "" && read.card?.card !== was) || !sameValues(valuesOf(read.card, key), valuesOf(shown.card, key))) {
      return { read, kept: true };
    }
  }
}

/**
 * People leads the list and the card: one of each at a time, each answer for the one the
 * window shows or dropped (a list typed into since, a card closed or another opened).
 */
export class People {
  private view: PeopleView = noPeople;
  /** Counts the lists asked and the cards opened: an answer for an earlier one is dropped. */
  private lists = 0;
  private cards = 0;

  constructor(
    private readonly calls: PeopleCalls,
    private readonly changed: (view: PeopleView) => void,
  ) {}

  snapshot(): PeopleView {
    return this.view;
  }

  private show(change: Partial<PeopleView>): void {
    this.view = { ...this.view, ...change };
    this.changed(this.view);
  }

  private card(change: Partial<CardView>): void {
    if (this.view.card !== undefined) {
      this.show({ card: { ...this.view.card, ...change } });
    }
  }

  /** reset closes the list and the card: the window left the profile. */
  reset(): void {
    this.lists++;
    this.cards++;
    this.show(noPeople);
  }

  /** openList opens the People list narrowed by query (words, and key:value field terms). */
  openList(query = ""): void {
    this.show({ list: { query, people: undefined, unreadable: [], note: "" } });
    this.find(query);
  }

  /** find narrows the open list by query, read afresh. */
  find(query: string): void {
    const list = this.view.list;
    if (list === undefined) {
      return;
    }
    const mine = ++this.lists;
    this.show({ list: { ...list, query } });
    this.calls.list(query).then(
      (r) => {
        const now = this.view.list;
        if (mine === this.lists && now !== undefined) {
          this.show({ list: { ...now, people: r.people, unreadable: r.unreadable, note: "" } });
        }
      },
      (err: unknown) => {
        const now = this.view.list;
        if (mine === this.lists && now !== undefined) {
          this.show({ list: { ...now, people: [], note: said(err) } });
        }
      },
    );
  }

  closeList(): void {
    this.lists++;
    this.show({ list: undefined });
  }

  /** openCard opens whom's card, read afresh; name is what the window calls them ("" its card's name). */
  openCard(who: Who, name: string): void {
    const mine = ++this.cards;
    this.show({ card: { who, name, read: undefined, adding: undefined, editing: undefined, busy: false, note: "" } });
    this.calls.card(who).then(
      (r) => {
        if (mine === this.cards) {
          // A DM's person is named by their ID from now on, as kithd found them.
          this.card({
            who: r.person !== "" ? { person: r.person } : who,
            name: name || r.name || r.card?.card || r.person,
            read: r,
          });
        }
      },
      (err: unknown) => {
        if (mine === this.cards) {
          this.card({ note: said(err) });
        }
      },
    );
  }

  closeCard(): void {
    this.cards++;
    this.show({ card: undefined });
  }

  /** edit opens key's field for writing, its values as the card holds them. */
  edit(key: string): void {
    const c = this.view.card;
    if (c?.read === undefined || c.busy) {
      return;
    }
    const field = holds(c.read.card, key);
    this.card({
      adding: undefined,
      editing: { key, text: valuesOf(c.read.card, key).join(", "), list: field.list, fresh: !field.has },
      note: "",
    });
  }

  typeEdit(text: string): void {
    const e = this.view.card?.editing;
    if (e !== undefined && this.view.card?.busy !== true) {
      this.card({ editing: { ...e, text } });
    }
  }

  cancelEdit(): void {
    if (this.view.card?.busy !== true) {
      this.card({ editing: undefined, adding: undefined });
    }
  }

  /** add offers the keys the folder's cards use that this one does not, then a new one. */
  add(): void {
    const c = this.view.card;
    if (c?.read === undefined || c.busy) {
      return;
    }
    const mine = this.cards;
    this.card({ editing: undefined, adding: { keys: undefined, naming: false, name: "" }, note: "" });
    this.calls.keys().then(
      (keys) => {
        const now = this.view.card;
        if (mine === this.cards && now?.adding !== undefined) {
          this.card({ adding: { ...now.adding, keys: keys.filter((k) => !holds(now.read?.card, k).has) } });
        }
      },
      (err: unknown) => {
        const now = this.view.card;
        if (mine === this.cards && now?.adding !== undefined) {
          this.card({ adding: { ...now.adding, keys: [] }, note: said(err) });
        }
      },
    );
  }

  /** pickKey asks the value of a key the folder's cards use. */
  pickKey(key: string): void {
    if (this.view.card?.adding !== undefined) {
      this.edit(key);
    }
  }

  /** nameKey asks the name of a new field. */
  nameKey(): void {
    const a = this.view.card?.adding;
    if (a !== undefined) {
      this.card({ adding: { ...a, naming: true } });
    }
  }

  typeKeyName(name: string): void {
    const a = this.view.card?.adding;
    if (a !== undefined) {
      this.card({ adding: { ...a, name } });
    }
  }

  /** submitKeyName asks the new field's value; no name puts Add a field away. */
  submitKeyName(): void {
    const key = this.view.card?.adding?.name.trim() ?? "";
    if (key === "") {
      this.card({ adding: undefined });
      return;
    }
    this.edit(key);
  }

  /**
   * save writes the field being edited: commas make a list, nothing clears it. A person
   * with no card gets one at the first value written.
   */
  save(): void {
    const c = this.view.card;
    const e = c?.editing;
    if (c?.read === undefined || e === undefined || c.busy) {
      return;
    }
    const values = valuesTyped(e.text);
    if (c.read.card?.card === "" && values.length === 0) {
      this.card({ editing: undefined });
      return;
    }
    const mine = this.cards;
    this.card({ busy: true, note: "" });
    writeField(this.calls, c.who, c.name, c.read, e.key, values, e.list).then(
      (w) => {
        if (mine === this.cards) {
          this.card({
            read: w.read,
            editing: undefined,
            busy: false,
            note: w.kept
              ? `${e.key} was changed elsewhere meanwhile, or the card is not the one shown: it is shown as it is now, and nothing was written.`
              : "",
          });
        }
      },
      (err: unknown) => {
        if (mine === this.cards) {
          this.card({ busy: false, note: said(err) });
        }
      },
    );
  }
}
