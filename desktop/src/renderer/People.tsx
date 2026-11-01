// People and their cards (people.ts) drawn: the People list over the window, narrowed as
// it is typed into, and a person's card: their identifiers and fields, each edited in
// place, Add a field, everything they said, and your conversation with them.
import { useEffect } from "react";

import type { CardField } from "../gen/backend/v1/services_pb";
import type { CardView, ListView } from "./people";
import type { Session } from "./session";

/** useEscape calls back on Escape while on: the dialog on top closes, or its edit does. */
function useEscape(on: boolean, back: () => void): void {
  useEffect(() => {
    if (!on) {
      return;
    }
    const key = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        back();
      }
    };
    window.addEventListener("keydown", key);
    return () => {
      window.removeEventListener("keydown", key);
    };
  }, [on, back]);
}

/** PeopleDialog is the People list (the TUI's :people): every card the profile shows, by name. */
export function PeopleDialog({ list, covered, session }: { list: ListView; covered: boolean; session: Session }) {
  useEscape(!covered, () => {
    session.closePeople();
  });
  const people = list.people;
  return (
    <div className="offer-backdrop">
      <section className="offer people" role="dialog" aria-modal="true" aria-label="People">
        <h2>People</h2>
        <input
          autoFocus
          type="search"
          aria-label="Narrow by name, or by a field (role:attorney)"
          placeholder="Name, or a field: role:attorney"
          value={list.query}
          onChange={(e) => {
            session.findPeople(e.target.value);
          }}
        />
        {people === undefined && <p className="detail">Reading the phonebook</p>}
        {people !== undefined && people.length === 0 && list.note === "" && (
          <p className="detail">
            {list.query.trim() === ""
              ? "The phonebook has no cards yet: a person's card makes one."
              : "No card matches."}
          </p>
        )}
        {people !== undefined && people.length > 0 && (
          <ul className="people-rows" aria-label="Cards">
            {people.map((p) => (
              <li key={p.card}>
                <button
                  type="button"
                  onClick={() => {
                    session.openPerson({ card: p.card }, p.card);
                  }}
                >
                  <span className="person-name" dir="auto">
                    {p.card}
                  </span>
                  {p.aliases.length > 0 && (
                    <span className="detail" dir="auto">
                      {p.aliases.join(", ")}
                    </span>
                  )}
                  {(p.matched.length > 0 || p.idKeys.length > 0) && (
                    <span className="detail person-keys" dir="auto">
                      {[...p.matched.map((m) => `${m.key}: ${m.values.join(", ")}`), p.idKeys.join(" · ")]
                        .filter((t) => t !== "")
                        .join(" · ")}
                    </span>
                  )}
                </button>
              </li>
            ))}
          </ul>
        )}
        {list.unreadable.length > 0 && (
          <p className="detail" role="status">
            Not read, their front matter is not YAML kith can read:{" "}
            {list.unreadable.map((u) => (
              <span key={u.card} title={u.reason} dir="auto">
                {u.card}.md{" "}
              </span>
            ))}
          </p>
        )}
        {list.note !== "" && (
          <p className="problem" role="alert">
            {list.note}
          </p>
        )}
        <p className="offer-actions">
          <button
            type="button"
            onClick={() => {
              session.closePeople();
            }}
          >
            Close
          </button>
        </p>
      </section>
    </div>
  );
}

/** PersonDialog is a person's card (the TUI's I). */
export function PersonDialog({ card, session }: { card: CardView; session: Session }) {
  useEscape(true, () => {
    if (card.editing !== undefined || card.adding !== undefined) {
      session.cancelField();
    } else {
      session.closePerson();
    }
  });
  const read = card.read;
  const file = read?.card?.card ?? "";
  return (
    <div className="offer-backdrop">
      <section className="offer person" role="dialog" aria-modal="true" aria-label={`About ${card.name}`}>
        <h2>
          <span dir="auto">{card.name}</span>{" "}
          <span className="detail person-file" dir="auto">
            {read === undefined ? "" : file !== "" ? `${file}.md` : "no card yet"}
          </span>
        </h2>
        {read === undefined && card.note === "" && <p className="detail">Reading the card</p>}
        {read !== undefined && (
          <>
            <dl className="card-fields" aria-busy={card.busy}>
              {[...(read.card?.ids ?? []), ...(read.card?.fields ?? [])].map((f) => (
                <Field key={f.key} field={f} card={card} session={session} />
              ))}
              {card.editing?.fresh === true && (
                <Field key={card.editing.key} field={undefined} card={card} session={session} />
              )}
            </dl>
            {file === "" && card.editing === undefined && (
              <p className="detail">A card is made, after them, the first time a field is written.</p>
            )}
            {card.adding !== undefined && <AddField card={card} session={session} />}
            <p className="person-actions">
              <button
                type="button"
                disabled={card.busy || card.editing !== undefined || card.adding !== undefined}
                onClick={() => {
                  session.addField();
                }}
              >
                Add a field
              </button>
              <button
                type="button"
                onClick={() => {
                  session.searchSaid(card.name, read.people);
                  session.closePerson();
                  session.closePeople();
                }}
              >
                Everything they said
              </button>
              {read.conversation !== "" && (
                <button
                  type="button"
                  onClick={() => {
                    session.closePerson();
                    session.closePeople();
                    session.open(read.conversation);
                  }}
                >
                  Your conversation
                </button>
              )}
            </p>
          </>
        )}
        {card.note !== "" && (
          <p className="problem" role="alert">
            {card.note}
          </p>
        )}
        <p className="offer-actions">
          <button
            type="button"
            onClick={() => {
              session.closePerson();
            }}
          >
            Close
          </button>
        </p>
      </section>
    </div>
  );
}

/** Field is one key of the card, edited in place: commas make a list, nothing clears it. */
function Field({ field, card, session }: { field: CardField | undefined; card: CardView; session: Session }) {
  const key = field?.key ?? card.editing?.key ?? "";
  const editing = card.editing?.key === key ? card.editing : undefined;
  if (editing === undefined) {
    return (
      <div className="card-field">
        <dt dir="auto">{key}</dt>
        <dd>
          <button
            type="button"
            className="field-value"
            title={`Edit ${key}`}
            disabled={card.busy || card.adding !== undefined}
            onClick={() => {
              session.editField(key);
            }}
          >
            <span dir="auto">{field?.values.join(", ") ?? ""}</span>
          </button>
        </dd>
      </div>
    );
  }
  return (
    <div className="card-field editing">
      <dt dir="auto">{key}</dt>
      <dd>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            session.saveField();
          }}
        >
          <input
            autoFocus
            aria-label={`${key}: commas make a list, nothing clears it`}
            dir="auto"
            value={editing.text}
            disabled={card.busy}
            onChange={(e) => {
              session.typeField(e.target.value);
            }}
          />
          <button type="submit" className="primary" disabled={card.busy}>
            {card.busy ? "Writing" : "Save"}
          </button>
          <button
            type="button"
            disabled={card.busy}
            onClick={() => {
              session.cancelField();
            }}
          >
            Cancel
          </button>
        </form>
        <p className="detail">Commas make a list; nothing clears it.</p>
      </dd>
    </div>
  );
}

/** AddField offers the keys the folder's cards use that this one does not, then a new one named. */
function AddField({ card, session }: { card: CardView; session: Session }) {
  const adding = card.adding;
  if (adding === undefined) {
    return null;
  }
  if (adding.naming) {
    return (
      <form
        className="add-field"
        onSubmit={(e) => {
          e.preventDefault();
          session.submitFieldKey();
        }}
      >
        <input
          autoFocus
          aria-label="The new field's name"
          placeholder="Its name: birthday, role, pets"
          value={adding.name}
          onChange={(e) => {
            session.typeFieldKey(e.target.value);
          }}
        />
        <button type="submit" className="primary">
          Next
        </button>
        <button
          type="button"
          onClick={() => {
            session.cancelField();
          }}
        >
          Cancel
        </button>
      </form>
    );
  }
  return (
    <section className="add-field" aria-label="Add a field">
      <h3>Add a field</h3>
      {adding.keys === undefined && <p className="detail">Reading the keys the cards use</p>}
      <ul className="key-rows">
        {(adding.keys ?? []).map((k) => (
          <li key={k}>
            <button
              type="button"
              onClick={() => {
                session.pickFieldKey(k);
              }}
            >
              <span dir="auto">{k}</span>
            </button>
          </li>
        ))}
        <li>
          <button
            type="button"
            onClick={() => {
              session.nameFieldKey();
            }}
          >
            New field <span className="detail">name it</span>
          </button>
        </li>
      </ul>
      <p className="offer-actions">
        <button
          type="button"
          onClick={() => {
            session.cancelField();
          }}
        >
          Cancel
        </button>
      </p>
    </section>
  );
}
