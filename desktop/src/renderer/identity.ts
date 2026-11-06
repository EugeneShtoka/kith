// The person an account is one of ([[display.identity]]): what the alias editor offers
// for their picture, and which it has.

import type { Identity } from "../gen/backend/v1/services_pb";

/** A picture a person may have: what the identity's picture is set to, and what to call it. */
export interface PictureChoice {
  value: string;
  label: string;
}

/** saved is a picture of their own kithd keeps ("file:<hash>"), not an account's. */
export function saved(picture: string): boolean {
  return picture.startsWith("file:");
}

/**
 * pictureChoices are the pictures person may have once account is one of theirs: each
 * account's own (another of theirs where one has none), one account's for all, or the
 * picture of their own they have, if any.
 */
export function pictureChoices(person: Identity | undefined, account: string): PictureChoice[] {
  const ids = [...(person?.ids ?? []).filter((id) => id !== account), account];
  const choices: PictureChoice[] = [{ value: "", label: "each account's own, else another of theirs" }];
  for (const id of ids) {
    choices.push({ value: id, label: `${id}'s, for all` });
  }
  if (person !== undefined && saved(person.picture)) {
    choices.push({ value: person.picture, label: "the picture you saved for them" });
  }
  return choices;
}
