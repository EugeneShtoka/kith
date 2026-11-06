// "Same person as…": an account made one of a person's ([[display.identity]]), as the
// terminal client's alias flow does, with their color and their picture.
import { useEffect, useState } from "react";

import type { Identity } from "../gen/backend/v1/services_pb";
import { pictureChoices, saved } from "./identity";
import { errorText, type Session } from "./session";

/** newPerson is the choice of a person not yet made; it is no alias, which is never empty. */
const newPerson = "";

/**
 * PersonEditor sets which person account (named name) is one of, their color and their
 * picture; onDone closes it, written or not.
 */
export function PersonEditor({
  account,
  name,
  session,
  onDone,
}: {
  account: string;
  name: string;
  session: Session;
  onDone: () => void;
}) {
  const [people, setPeople] = useState<Identity[] | undefined>(undefined);
  const [alias, setAlias] = useState(newPerson);
  const [typed, setTyped] = useState(name);
  const [color, setColor] = useState("");
  const [picture, setPicture] = useState("");
  const [problem, setProblem] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    let live = true;
    session.identities().then(
      (got) => {
        if (!live) {
          return;
        }
        setPeople(got);
        const theirs = got.find((p) => p.ids.includes(account));
        if (theirs !== undefined) {
          setAlias(theirs.alias);
          setColor(theirs.color);
          setPicture(theirs.picture);
        }
      },
      (err: unknown) => {
        if (live) {
          setProblem(`Could not read the people: ${errorText(err)}`);
          setPeople([]);
        }
      },
    );
    return () => {
      live = false;
    };
  }, [session, account]);

  const person = people?.find((p) => p.alias === alias && alias !== newPerson);
  const choices = pictureChoices(person, account);
  if (saved(picture) && !choices.some((c) => c.value === picture)) {
    choices.push({ value: picture, label: "the picture you just saved" });
  }
  const pickPerson = (value: string) => {
    const chosen = people?.find((p) => p.alias === value);
    setAlias(value);
    setColor(chosen?.color ?? "");
    setPicture(chosen?.picture ?? "");
  };
  const save = async () => {
    const as = alias === newPerson ? typed.trim() : alias;
    if (as === "") {
      setProblem("Give them a name.");
      return;
    }
    setBusy(true);
    try {
      await session.setIdentity(account, as, color, picture);
      onDone();
    } catch (err) {
      setProblem(`Not saved: ${errorText(err)}`);
      setBusy(false);
    }
  };
  const upload = async () => {
    setBusy(true);
    try {
      const name = await session.pickPicture();
      if (name !== undefined) {
        setPicture(name);
      }
    } catch (err) {
      setProblem(`That picture was not kept: ${errorText(err)}`);
    } finally {
      setBusy(false);
    }
  };

  if (people === undefined) {
    return <p className="detail">Reading the people…</p>;
  }
  return (
    <form
      className="person-editor"
      aria-label="Same person as"
      onSubmit={(e) => {
        e.preventDefault();
        void save();
      }}
    >
      <fieldset disabled={busy}>
        <label>
          <span>Same person as</span>
          <select
            value={alias}
            onChange={(e) => {
              pickPerson(e.target.value);
            }}
          >
            <option value={newPerson}>a new person</option>
            {people.map((p) => (
              <option key={p.alias} value={p.alias}>
                {p.alias} ({p.ids.length} {p.ids.length === 1 ? "account" : "accounts"})
              </option>
            ))}
          </select>
        </label>
        {alias === newPerson && (
          <label>
            <span>Named</span>
            <input
              value={typed}
              dir="auto"
              onChange={(e) => {
                setTyped(e.target.value);
              }}
            />
          </label>
        )}
        <label>
          <input
            type="checkbox"
            checked={color !== ""}
            onChange={(e) => {
              setColor(e.target.checked ? "#1f6feb" : "");
            }}
          />
          <span>Their own color</span>
          {color !== "" && (
            <input
              type="color"
              aria-label="Their color"
              value={/^#[0-9a-f]{6}$/i.test(color) ? color : "#1f6feb"}
              onChange={(e) => {
                setColor(e.target.value);
              }}
            />
          )}
        </label>
        <fieldset className="picture-choices">
          <legend>Their picture</legend>
          {choices.map((c) => (
            <label key={c.value}>
              <input
                type="radio"
                name="picture"
                checked={picture === c.value}
                onChange={() => {
                  setPicture(c.value);
                }}
              />
              <span dir="auto">{c.label}</span>
            </label>
          ))}
          <button type="button" onClick={() => void upload()}>
            A picture of your own…
          </button>
        </fieldset>
        {problem && <p className="problem">{problem}</p>}
        <button type="submit">Save</button>{" "}
        <button type="button" onClick={onDone}>
          Cancel
        </button>
      </fieldset>
    </form>
  );
}
