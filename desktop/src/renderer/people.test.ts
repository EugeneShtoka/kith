import { create } from "@bufbuild/protobuf";
import fc from "fast-check";
import { describe, expect, it } from "vitest";

import {
  type PeopleResponse,
  PeopleResponseSchema,
  type PersonCardResponse,
  PersonCardResponseSchema,
  type PhonebookCard,
  PhonebookCardSchema,
} from "../gen/backend/v1/services_pb";
import { type PeopleCalls, People, type PeopleView, valuesOf, valuesTyped, type Who, writeField } from "./people";

const aborted = () =>
  Promise.reject(new Error("Error invoking remote method 'kith:call': ConnectError: [aborted] the card changed"));
const notFound = (what: string) =>
  Promise.reject(new Error(`Error invoking remote method 'kith:call': ConnectError: [not_found] ${what}`));

interface Card {
  /** Its fields, by key: values, and whether written as a list. */
  fields: Map<string, { values: string[]; list: boolean }>;
  /** The people it holds an identifier of. */
  holds: Set<string>;
  revision: number;
}

/**
 * Folder is kithd over a phonebook folder, as SetPersonField writes it: over the card at
 * the revision the window read (else ABORTED), a person's card being the first by name
 * holding them, a new card made for a person with none. before runs as each write
 * reaches kithd: another writer, between the window's read and its write.
 */
class Folder implements PeopleCalls {
  cards = new Map<string, Card>();
  revisions = 0;
  writes = 0;
  readonly before: (() => void)[] = [];

  add(name: string, person: string, fields: Record<string, string[]> = {}): void {
    this.cards.set(name, {
      fields: new Map(Object.entries(fields).map(([k, v]) => [k, { values: v, list: true }])),
      holds: new Set([person]),
      revision: ++this.revisions,
    });
  }

  /** of is the person's card: the first, by name, holding them. */
  of(person: string): string | undefined {
    return [...this.cards.keys()].sort().find((n) => this.cards.get(n)?.holds.has(person));
  }

  private named(who: Who): { name: string | undefined; missing: boolean } {
    if ("person" in who) {
      return { name: this.of(who.person), missing: false };
    }
    if ("card" in who) {
      return { name: this.cards.has(who.card) ? who.card : undefined, missing: !this.cards.has(who.card) };
    }
    return { name: undefined, missing: true };
  }

  private shown(name: string | undefined): PhonebookCard {
    const c = name === undefined ? undefined : this.cards.get(name);
    return create(PhonebookCardSchema, {
      card: name ?? "",
      revision: c === undefined ? "" : String(c.revision),
      fields: [...(c?.fields ?? [])].map(([key, f]) => ({ key, values: f.values, list: f.list })),
    });
  }

  list(): Promise<PeopleResponse> {
    return Promise.resolve(create(PeopleResponseSchema, {}));
  }

  card(who: Who): Promise<PersonCardResponse> {
    const { name, missing } = this.named(who);
    if (missing) {
      return notFound("the phonebook has no card");
    }
    return Promise.resolve(
      create(PersonCardResponseSchema, { card: this.shown(name), person: "person" in who ? who.person : "" }),
    );
  }

  set(who: Who, name: string, revision: string, key: string, values: string[], list: boolean): Promise<PhonebookCard> {
    this.before.shift()?.();
    const { name: now, missing } = this.named(who);
    if (missing) {
      return notFound("the phonebook has no card");
    }
    const card = now === undefined ? undefined : this.cards.get(now);
    if ((card === undefined ? "" : String(card.revision)) !== revision) {
      return aborted();
    }
    if (card === undefined && values.length === 0) {
      return Promise.resolve(this.shown(undefined)); // nothing to clear on no card
    }
    this.writes++;
    let at = now;
    if (card === undefined) {
      at = this.free(name);
      this.add(at, "person" in who ? who.person : "");
    }
    const written = this.cards.get(at ?? "");
    if (written === undefined) {
      throw new Error("no card to write");
    }
    if (values.length === 0) {
      written.fields.delete(key);
    } else {
      written.fields.set(key, { values, list: list || values.length > 1 });
    }
    written.revision = ++this.revisions;
    return Promise.resolve(this.shown(at));
  }

  keys(): Promise<readonly string[]> {
    return Promise.resolve([...new Set([...this.cards.values()].flatMap((c) => [...c.fields.keys()]))].sort());
  }

  /** free is name, numbered past the cards that have it. */
  private free(name: string): string {
    let at = name;
    for (let n = 2; this.cards.has(at); n++) {
      at = `${name} ${String(n)}`;
    }
    return at;
  }
}

/** What another writer does: the key the window writes, another key, the card itself. */
type Meddle = "same key" | "other key" | "body" | "renamed" | "removed" | "second card" | "made" | "window same";

/**
 * meddle has folder's other writer do m to Dana's card, and says what it came to: with
 * no card there, a card made for her ("made"), or nothing (undefined).
 */
function meddle(folder: Folder, m: Meddle, n: number): Meddle | undefined {
  const name = folder.of("dana");
  const card = name === undefined ? undefined : folder.cards.get(name);
  if (name === undefined || card === undefined) {
    if (m !== "made" && m !== "second card") {
      return undefined;
    }
    folder.add("Dana (TUI)", "dana", { met: ["at work"] });
    return "made";
  }
  const bump = () => {
    card.revision = ++folder.revisions;
  };
  switch (m) {
    case "same key":
    case "window same":
      card.fields.set("pets", { values: [`other ${String(n)}`], list: true });
      bump();
      break;
    case "other key":
      card.fields.set(`origin${String(n)}`, { values: ["Haifa"], list: false });
      bump();
      break;
    case "body":
      bump();
      break;
    case "renamed":
      folder.cards.delete(name);
      folder.cards.set(`${name} renamed`, card);
      bump();
      break;
    case "removed":
      folder.cards.delete(name);
      break;
    case "second card":
      folder.add(`A${String(n)}`, "dana", { note: ["first by name"] });
      break;
    case "made":
      return undefined; // she has a card: none is made
  }
  return m;
}

const meddles: Meddle[] = ["same key", "other key", "body", "renamed", "removed", "second card", "made", "window same"];

describe("a field written from a card", () => {
  it("lands over the card as read, keeps every other writer's change, and never writes over one of the same key", async () => {
    await fc.assert(
      fc.asyncProperty(
        fc.boolean(),
        fc.boolean(),
        fc.array(fc.constantFrom(...meddles), { maxLength: 3 }),
        fc.array(fc.string({ minLength: 1, maxLength: 3 }), { maxLength: 3 }),
        async (hasCard, byCard, happened, values) => {
          const folder = new Folder();
          if (hasCard) {
            folder.add("Dana Levi", "dana", { pets: ["Rex"] });
          }
          const who: Who = hasCard && byCard ? { card: "Dana Levi" } : { person: "dana" };
          const shown = await folder.card(who);
          // What each meddle came to, as the writes reached it.
          const did = new Map<number, Meddle>();
          happened.forEach((m, n) => {
            folder.before.push(() => {
              const came = meddle(folder, m, n);
              if (came !== undefined) {
                did.set(n, came);
              }
            });
          });
          // What the window is to write, and what another writer changed meanwhile.
          let thrown: unknown;
          let got: Awaited<ReturnType<typeof writeField>> | undefined;
          try {
            got = await writeField(folder, who, "Dana", shown, "pets", values, true);
          } catch (err) {
            thrown = err;
          }
          const seen = [...did.values()];
          const sameKey = seen.some((m) => m === "same key" || m === "window same");
          const named = "card" in who;
          const anotherCard =
            hasCard && seen.some((m) => m === "removed" || (!named && (m === "renamed" || m === "second card")));
          if (named && seen.some((m) => m === "renamed" || m === "removed")) {
            expect(String(thrown)).toContain("[not_found]");
            return;
          }
          expect(thrown).toBeUndefined();
          // The card the window names: the one it read, or her card now.
          const now = "card" in who ? who.card : folder.of("dana");
          const card = now === undefined ? undefined : folder.cards.get(now);
          if (sameKey || anotherCard) {
            expect(got?.kept).toBe(true);
            // Nothing of the window's written: the key holds what the other writer left.
            if (card !== undefined && values.length > 0 && !sameKey) {
              expect(card.fields.get("pets")?.values).not.toEqual(values);
            }
            expect(got?.read.card?.card ?? "").toBe(now ?? "");
            return;
          }
          expect(got?.kept).toBe(false);
          expect(card?.fields.get("pets")?.values ?? []).toEqual(values);
          // Every other writer's own key is still on a card, unless a card was removed since.
          const somewhere = (has: (c: Card) => boolean) => [...folder.cards.values()].some(has);
          did.forEach((m, n) => {
            if ([...did].some(([later, other]) => later > n && other === "removed")) {
              return;
            }
            if (m === "other key") {
              expect(somewhere((c) => c.fields.has(`origin${String(n)}`))).toBe(true);
            }
            if (m === "made") {
              expect(somewhere((c) => c.fields.get("met")?.values[0] === "at work")).toBe(true);
            }
          });
          expect(valuesOf(got?.read.card, "pets")).toEqual(values);
        },
      ),
      { numRuns: 400 },
    );
  });

  it("splits what is typed at commas, blanks dropped", () => {
    expect(valuesTyped(" Rex, Mia ,, ")).toEqual(["Rex", "Mia"]);
    expect(valuesTyped("")).toEqual([]);
    expect(valuesTyped("[[Eli Levi]]")).toEqual(["[[Eli Levi]]"]);
  });
});

/** settle lets the calls answer. */
const settle = () => new Promise((resolve) => setTimeout(resolve, 0));

describe("people and their cards", () => {
  it("drops an answer for a card closed or replaced, and for a list typed into since", async () => {
    const folder = new Folder();
    folder.add("Dana Levi", "dana", { pets: ["Rex"] });
    folder.add("Eli", "eli");
    const held: (() => void)[] = [];
    const slow: PeopleCalls = {
      list: (q) =>
        new Promise((resolve) => {
          held.push(() => {
            resolve(create(PeopleResponseSchema, { people: [{ card: q }] }));
          });
        }),
      card: (who) =>
        new Promise((resolve, reject) => {
          held.push(() => {
            folder.card(who).then(resolve, reject);
          });
        }),
      set: folder.set.bind(folder),
      keys: folder.keys.bind(folder),
    };
    let view: PeopleView | undefined;
    const p = new People(slow, (v) => (view = v));
    p.openCard({ person: "dana" }, "Dana");
    p.openCard({ person: "eli" }, "Eli");
    p.openList("d");
    p.find("da");
    for (const answer of held.splice(0).reverse()) {
      answer();
    }
    await settle();
    expect(view?.card?.name).toBe("Eli");
    expect(view?.card?.read?.card?.card).toBe("Eli");
    expect(view?.list?.people?.map((x) => x.card)).toEqual(["da"]);
    p.openCard({ person: "dana" }, "Dana");
    p.reset(); // the window left the profile
    for (const answer of held.splice(0).reverse()) {
      answer();
    }
    await settle();
    expect(view).toEqual({ list: undefined, card: undefined });
  });

  it("edits a field in place, offers the folder's other keys to add, and makes a card at the first value", async () => {
    const folder = new Folder();
    folder.add("Eli", "eli", { role: ["architect"], pets: ["Bo"] });
    let view: PeopleView | undefined;
    const p = new People(folder, (v) => (view = v));
    p.openCard({ person: "dana" }, "Dana Levi");
    await settle();
    expect(view?.card?.read?.card?.card).toBe("");
    p.add();
    await settle();
    expect(view?.card?.adding?.keys).toEqual(["pets", "role"]);
    p.pickKey("role");
    p.save(); // nothing typed: no card is made for nothing
    await settle();
    expect(folder.writes).toBe(0);
    p.add();
    p.nameKey();
    p.typeKeyName("  met ");
    p.submitKeyName();
    expect(view?.card?.editing).toMatchObject({ key: "met", fresh: true });
    p.typeEdit("at work, at home");
    p.save();
    await settle();
    expect(folder.cards.get("Dana Levi")?.fields.get("met")).toEqual({ values: ["at work", "at home"], list: true });
    expect(view?.card?.read?.card?.card).toBe("Dana Levi");
    expect(view?.card?.editing).toBeUndefined();
    p.add(); // the keys it holds now are not offered again
    await settle();
    expect(view?.card?.adding?.keys).toEqual(["pets", "role"]);
    p.cancelEdit();
    p.edit("met");
    expect(view?.card?.editing?.text).toBe("at work, at home");
    p.typeEdit("");
    p.save();
    await settle();
    expect(folder.cards.get("Dana Levi")?.fields.has("met")).toBe(false);
  });

  it("says so when another writer changed the field first, and keeps theirs", async () => {
    const folder = new Folder();
    folder.add("Dana Levi", "dana", { pets: ["Rex"] });
    let view: PeopleView | undefined;
    const p = new People(folder, (v) => (view = v));
    p.openCard({ card: "Dana Levi" }, "Dana Levi");
    await settle();
    p.edit("pets");
    p.typeEdit("Rex, Mia");
    folder.before.push(() => {
      meddle(folder, "same key", 0);
    });
    p.save();
    await settle();
    expect(folder.cards.get("Dana Levi")?.fields.get("pets")?.values).toEqual(["other 0"]);
    expect(valuesOf(view?.card?.read?.card, "pets")).toEqual(["other 0"]);
    expect(view?.card?.note).toContain("changed elsewhere");
    expect(view?.card?.busy).toBe(false);
  });
});
