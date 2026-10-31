// A face for a person or a room until pictures come: their initials, in their color.
import { hueOf, initials } from "./faces";

/** Avatar draws id's face, named name, in hue's class (a person's color; else id's hashed hue); small in the room list. */
export function Avatar({ id, name, small = false, hue }: { id: string; name: string; small?: boolean; hue?: string }) {
  return (
    <span className={`avatar ${hue ?? `hue-${String(hueOf(id))}`}${small ? " small" : ""}`} aria-hidden="true">
      {initials(name)}
    </span>
  );
}
