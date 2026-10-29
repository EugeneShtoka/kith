// Which profile the window runs in, decided alike by main (the profile every request
// carries) and the window (what it shows), from the same DeviceService answers.

/**
 * runIn is the profile a window running in current ("" none yet) runs in once the relay
 * says the config has profiles and remembers remembered ("" none, or one the config
 * lacks): current while the config still has it (a window does not jump when the
 * device file changes under it), else the one remembered, else the only one there is;
 * "" when there are several and none is remembered, and the window asks. A name is
 * returned as the config spells it.
 */
export function runIn(current: string, remembered: string, profiles: readonly string[]): string {
  const kept = profiles.find((p) => p.toLowerCase() === current.trim().toLowerCase());
  if (current !== "" && kept !== undefined) {
    return kept;
  }
  if (remembered !== "") {
    return remembered;
  }
  return profiles.length === 1 ? (profiles[0] ?? "") : "";
}
