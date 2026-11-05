// A face for a person or a room: their picture where kithd keeps one, else their
// initials, in their color.
import { useState } from "react";

import { hueOf, initials } from "./faces";

/**
 * Avatar draws id's face, named name, in hue's class (a person's color; else id's hashed
 * hue); small in the room list. picture is its kith-media: URL; one that does not load
 * shows the initials.
 */
export function Avatar({
  id,
  name,
  small = false,
  hue,
  picture,
}: {
  id: string;
  name: string;
  small?: boolean;
  hue?: string;
  picture?: string;
}) {
  const [broken, setBroken] = useState("");
  const shown = picture !== undefined && picture !== broken;
  return (
    <span
      className={`avatar ${hue ?? `hue-${String(hueOf(id))}`}${small ? " small" : ""}${shown ? " pictured" : ""}`}
      aria-hidden="true"
    >
      {shown ? (
        <img
          src={picture}
          alt=""
          loading="lazy"
          onError={() => {
            setBroken(picture);
          }}
        />
      ) : (
        initials(name)
      )}
    </span>
  );
}
