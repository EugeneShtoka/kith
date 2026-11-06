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
};

/** linear is hex's channels as light, not as encoded. */
function linear(hex: string): number[] {
  return [1, 3, 5].map((i) => {
    const c = parseInt(hex.slice(i, i + 2), 16) / 255;
    return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  });
}

/** okL is hex's OKLab lightness, as the stylesheet's oklch(from …) reads it. */
function okL(hex: string): number {
  const [r = 0, g = 0, b = 0] = linear(hex);
  const l = Math.cbrt(0.4122214708 * r + 0.5363325363 * g + 0.0514459929 * b);
  const m = Math.cbrt(0.2119034982 * r + 0.6806995451 * g + 0.1073969566 * b);
  const s = Math.cbrt(0.0883024619 * r + 0.2817188376 * g + 0.6299787005 * b);
  return 0.2104542553 * l + 0.793617785 * m - 0.0040720804 * s;
}

/** contrast is the WCAG contrast of hex against black (dark) or white. */
function contrast(hex: string, dark: boolean): number {
  const [r = 0, g = 0, b = 0] = linear(hex);
  const y = 0.2126 * r + 0.7152 * g + 0.0722 * b;
  return dark ? (y + 0.05) / 0.05 : 1.05 / (y + 0.05);
}

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

  it("color the bars, buttons and lines from the roles, so a theme file colors them too", () => {
    for (const name of ["--surface-2", "--raised", "--line"]) {
      for (const [mode, block] of Object.entries({ light: css, dark })) {
        expect(new RegExp(`\\s${name}: ([^;]*);`).exec(block)?.[1], `${mode} ${name}`).toMatch(/var\(--(bg|surface)\)/);
      }
    }
  });

  it("draw each badge's count readably on its unread and mention color", () => {
    const threshold = Number(/clamp\(0, \(([\d.]+) - l\)/.exec(css)?.[1]);
    expect(threshold).toBeGreaterThan(0);
    for (const name of ["light", "dark", "cobalt"]) {
      for (const role of ["unread", "mention"]) {
        const hex = file(name)[role] ?? "";
        expect(contrast(hex, okL(hex) > threshold), `${name} ${role}`).toBeGreaterThanOrEqual(4.5);
      }
    }
  });
});
