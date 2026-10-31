// A face for a person or a room until pictures come: their initials, in their color.
import { hueOf, initials } from "./faces";

/** Avatar draws id's face, named name; small in the room list. */
export function Avatar({ id, name, small = false }: { id: string; name: string; small?: boolean }) {
  return (
    <span className={`avatar hue-${String(hueOf(id))}${small ? " small" : ""}`} aria-hidden="true">
      {initials(name)}
    </span>
  );
}
