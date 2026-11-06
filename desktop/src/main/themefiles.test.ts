import { readFileSync } from "node:fs";
import path from "node:path";

import { describe, expect, it } from "vitest";

/** The stylesheet's token for each role (the renderer's theme.roles). */
const roles = {
  accent: "--accent",
  background: "--bg",
  surface: "--surface",
  text: "--text",
  muted: "--muted",
  link: "--link",
  selection: "--accent-soft",
  unread: "--unread",
  mention: "--mention",
  bar: "--surface-2",
  raised: "--raised",
  border: "--line",
  unread_text: "--unread-fg",
  mention_text: "--mention-fg",
};

describe("the shipped light and dark themes", () => {
  // The stylesheet draws the window before the theme is read: its colors are the files'.
  const file = (name: string) =>
    Object.fromEntries(
      [
        ...readFileSync(
          path.join(import.meta.dirname, "../../../internal/config/themes", `${name}.toml`),
          "utf8",
        ).matchAll(/^(\w+) = "(#[0-9a-f]{6})"$/gm),
      ].map(([, role = "", value = ""]) => [role, value]),
    );
  const css = readFileSync(path.join(import.meta.dirname, "../renderer/styles.css"), "utf8");
  const dark = css.slice(css.indexOf("@media (prefers-color-scheme: dark)"));
  const token = (block: string, name: string) => new RegExp(`\\s${name}: (#[0-9a-f]{6});`).exec(block)?.[1];

  it("are the stylesheet's own colors, role by role", () => {
    for (const [role, name] of Object.entries(roles)) {
      expect(token(css, name), `light ${role}`).toBe(file("light")[role]);
      expect(token(dark, name), `dark ${role}`).toBe(file("dark")[role]);
    }
  });
});
