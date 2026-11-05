// The window's pictures are never remote URLs: each is a kith-media: URL that main
// answers with the attachment's bytes, from kithd's LoadImage, or a room's picture or a
// person's in it, from LoadPicture.

export const mediaScheme = "kith-media";

/**
 * mediaURL names a message's attachment: its room and event, and version, what the
 * attachment is now. An edit that replaces a picture changes version, so the URL (the
 * key main caches the bytes under, and the browser's) changes with the content.
 */
export function mediaURL(room: string, event: string, version: string): string {
  const path = [room, event].map(encodeURIComponent).join("/");
  return `${mediaScheme}://media/${path}?v=${encodeURIComponent(version)}`;
}

/**
 * pictureURL names a room's picture at version (Room.picture), or with person that
 * person's in the room (SenderPictures), which changes with the picture, as mediaURL's
 * does with an attachment.
 */
export function pictureURL(room: string, version: string, person = ""): string {
  const path = (person === "" ? [room] : [room, person]).map(encodeURIComponent).join("/");
  return `${mediaScheme}://picture/${path}?v=${encodeURIComponent(version)}`;
}

/**
 * pictureOf is the room a kith-media: picture URL names, and the person in it ("" for
 * the room's own), or undefined for any other URL.
 */
export function pictureOf(url: string): { room: string; person: string } | undefined {
  let parsed: URL;
  try {
    parsed = new URL(url);
  } catch {
    return undefined;
  }
  const parts = parsed.pathname.split("/").slice(1);
  if (parsed.protocol !== `${mediaScheme}:` || parsed.host !== "picture" || parts.length < 1 || parts.length > 2) {
    return undefined;
  }
  try {
    const [room = "", person] = parts.map(decodeURIComponent);
    return room !== "" && person !== "" ? { room, person: person ?? "" } : undefined;
  } catch {
    return undefined;
  }
}

/** mediaOf is the room and event a kith-media: URL names, or undefined for any other URL. */
export function mediaOf(url: string): { room: string; event: string } | undefined {
  let parsed: URL;
  try {
    parsed = new URL(url);
  } catch {
    return undefined;
  }
  if (parsed.protocol !== `${mediaScheme}:` || parsed.host !== "media") {
    return undefined;
  }
  const parts = parsed.pathname.split("/").slice(1);
  if (parts.length !== 2) {
    return undefined;
  }
  try {
    const [room, event] = parts.map(decodeURIComponent);
    return room && event ? { room, event } : undefined;
  } catch {
    return undefined;
  }
}
