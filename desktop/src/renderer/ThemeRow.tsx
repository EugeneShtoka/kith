// One key of the theme as the settings page and a sender's color entry offer it: its
// value, written for every device unless "only this device" is on (device.toml), and a
// reset that takes the key out of the file it is in.
import { useEffect, useRef } from "react";

import { type ThemeResponse, ThemeScope } from "../gen/device/v1/device_pb";
import { themeModes } from "../shared/ipc";
import { layer, valueOf } from "./theme";
import type { Session } from "./session";

/** builtIn is a token's value in styles.css, its dark one for dark: what a role is with none picked. */
export function builtIn(token: string, dark: boolean): string {
  let light = "";
  let darker = "";
  const read = (rules: CSSRuleList, inDark: boolean) => {
    for (const rule of Array.from(rules)) {
      if (rule instanceof CSSMediaRule) {
        read(rule.cssRules, rule.media.mediaText.includes("dark"));
      } else if (rule instanceof CSSStyleRule && rule.selectorText === ":root") {
        const value = rule.style.getPropertyValue(token).trim();
        if (value !== "") {
          if (inDark) {
            darker = value;
          } else {
            light = value;
          }
        }
      }
    }
  };
  for (const sheet of Array.from(document.styleSheets)) {
    read(sheet.cssRules, false);
  }
  return (dark && darker) || light;
}

/** hex is value as a color input takes it, black for none. */
function hex(value: string): string {
  return /^#[0-9a-f]{6}$/i.test(value) ? value.toLowerCase() : "#000000";
}

/** The row's scope: this device's file where it sets the key, else every device's. */
function scopeOf(theme: ThemeResponse, key: string): ThemeScope.SHARED | ThemeScope.DEVICE {
  return layer(theme, key, ThemeScope.DEVICE) !== "" ? ThemeScope.DEVICE : ThemeScope.SHARED;
}

/** OnlyHere is the row's "only this device": on, the key is this device's (device.toml); off, its override goes. */
function OnlyHere({ theme, k, value, session }: { theme: ThemeResponse; k: string; value: string; session: Session }) {
  const own = layer(theme, k, ThemeScope.DEVICE) !== "";
  return (
    <label className="detail">
      <input
        type="checkbox"
        checked={own}
        onChange={(e) => {
          void session.changeTheme(k, ThemeScope.DEVICE, e.target.checked ? value : "");
        }}
      />{" "}
      only this device
    </label>
  );
}

/** Reset takes the key out of the file the row writes; shown only where that file sets it. */
function Reset({ theme, k, session }: { theme: ThemeResponse; k: string; session: Session }) {
  const scope = scopeOf(theme, k);
  if (layer(theme, k, scope) === "") {
    return null;
  }
  return (
    <button
      type="button"
      onClick={() => {
        void session.changeTheme(k, scope, "");
      }}
    >
      {scope === ThemeScope.DEVICE ? "Reset here" : "Reset"}
    </button>
  );
}

/**
 * ColorRow is one color of the theme: k its key, fallback what it draws as with none
 * picked. The color is written once picked (the input's change), not while dragged.
 */
export function ColorRow({
  label,
  k,
  theme,
  fallback,
  session,
}: {
  label: string;
  k: string;
  theme: ThemeResponse;
  fallback: string;
  session: Session;
}) {
  const value = valueOf(theme, k) || fallback;
  const input = useRef<HTMLInputElement>(null);
  useEffect(() => {
    const el = input.current;
    if (el === null) {
      return;
    }
    const picked = () => {
      void session.changeTheme(k, scopeOf(theme, k), el.value);
    };
    el.addEventListener("change", picked);
    return () => {
      el.removeEventListener("change", picked);
    };
  }, [session, theme, k, value]);
  return (
    <div className="setting-row theme-row">
      {/* Uncontrolled, drawn afresh (key) when the theme says otherwise: dragging writes nothing. */}
      <input key={value} ref={input} type="color" aria-label={label} defaultValue={hex(value)} />
      <span dir="auto">{label}</span>
      <OnlyHere theme={theme} k={k} value={hex(value)} session={session} />
      <Reset theme={theme} k={k} session={session} />
    </div>
  );
}

/** ModeRow is the theme's mode: the system's light or dark, or one of them. */
export function ModeRow({ theme, session }: { theme: ThemeResponse; session: Session }) {
  const mode = valueOf(theme, "mode") || "system";
  return (
    <div className="setting-row theme-row">
      <select
        aria-label="Mode"
        value={mode}
        onChange={(e) => {
          void session.changeTheme("mode", scopeOf(theme, "mode"), e.target.value);
        }}
      >
        {themeModes.map((m) => (
          <option key={m} value={m}>
            {m === "system" ? "as the system is" : m}
          </option>
        ))}
      </select>
      <span>Mode</span>
      <OnlyHere theme={theme} k="mode" value={mode} session={session} />
      <Reset theme={theme} k="mode" session={session} />
    </div>
  );
}
