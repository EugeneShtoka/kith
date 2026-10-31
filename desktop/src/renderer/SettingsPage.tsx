// The settings page: this device's settings shared with the TUI (device.toml), and the
// window's theme (Session.changeTheme), each change written at once (no Save button); and the
// network accounts kithd logs in (Accounts.tsx).
import { useEffect, useState } from "react";

import type { DeviceSettings, ThemeResponse, ReadRule } from "../gen/device/v1/device_pb";
import { fontSizes } from "../shared/ipc";
import { Accounts } from "./Accounts";
import { VerifyStart } from "./Verification";
import { networkMark } from "./faces";
import { picked, roles, valueOf } from "./theme";
import { builtIn, ColorRow, ModeRow } from "./ThemeRow";
import type { Session, SettingsPage as Page, View } from "./session";
import { type Key, labels } from "./settings";

/** The skin tones, as their modifiers ("" none), named as device.toml names them. */
const tones: [string, string][] = [
  ["", "none"],
  ["\u{1F3FB}", "light"],
  ["\u{1F3FC}", "medium-light"],
  ["\u{1F3FD}", "medium"],
  ["\u{1F3FE}", "medium-dark"],
  ["\u{1F3FF}", "dark"],
];
const emojiSets = ["curated", "standard", "complete"];
const scopes: [string, string][] = [
  ["room", "this conversation, then its space, then everywhere"],
  ["space", "the space, then everywhere"],
  ["global", "one ranking everywhere"],
  ["static", "no ranking: the list below"],
];

/** The words [spell] and [complete] take, each with what it means. */
const spellUnderlines: [string, string][] = [
  ["curly", ""],
  ["line", ""],
  ["none", "still checked, for alt+S"],
];
const rareUnderlines: [string, string][] = [
  ["dotted", ""],
  ["line", ""],
  ["none", ""],
];
const autocorrects: [string, string][] = [
  ["off", ""],
  ["misspellings", "a word with one certain fix"],
  ["rare", "a rare word kithd is sure of"],
  ["all", "both"],
];
const completeScopes: [string, string][] = [
  ["room", "this conversation first"],
  ["space", "its space first"],
  ["global", "everywhere alike"],
];

/** A read rule's send, as the page offers it. */
type Send = "inherit" | "announce" | "private";

function sendOf(rule: ReadRule): Send {
  if (rule.send === undefined) {
    return "inherit";
  }
  return rule.send ? "announce" : "private";
}

function sendFrom(send: Send): boolean | undefined {
  return send === "inherit" ? undefined : send === "announce";
}

export function SettingsPage({ page, view, session }: { page: Page; view: View; session: Session }) {
  const s = page.read?.settings;
  return (
    <section className="settings" aria-label="Settings">
      <header>
        <h2>Settings</h2>
        <button
          type="button"
          onClick={() => {
            session.closeSettings();
          }}
        >
          Done
        </button>
      </header>
      <p className="detail">
        This device's settings, shared with the terminal client (device.toml). Each change is saved at once.
      </p>
      {page.note && <p className="problem">{page.note}</p>}
      {s === undefined ? (
        !page.note && <p className="banner">Reading the settings…</p>
      ) : (
        <Fields s={s} busy={page.busy} session={session} />
      )}
      <Theme theme={view.theme} change={view.themeChange} session={session} />
      <WindowScale />
      <Accounts login={view.login} rail={view.rail} session={session} />
      {view.seat.state === "seated" && <VerifyStart accounts={view.accounts} verify={view.verify} session={session} />}
    </section>
  );
}

/**
 * Theme is the window's theme: its mode, each mode's colors, each account's and each
 * picked person's. A row writes every device's (the bridge's config) unless its "only
 * this device" is on.
 */
function Theme({
  theme,
  change,
  session,
}: {
  theme: ThemeResponse | undefined;
  change: View["themeChange"];
  session: Session;
}) {
  const dark = window.matchMedia("(prefers-color-scheme: dark)").matches;
  const accounts = new Map<string, string>();
  for (const a of theme?.accounts ?? []) {
    accounts.set(`accounts.${a.on}`, `${a.network} ${a.name}`);
  }
  const keys = Object.keys(theme?.values ?? {});
  for (const k of keys) {
    if (theme !== undefined && k.startsWith("accounts.") && !accounts.has(k) && picked(theme, k)) {
      accounts.set(k, k.slice("accounts.".length)); // an account kithd no longer serves
    }
  }
  const people = keys.filter((k) => theme !== undefined && k.startsWith("people.") && picked(theme, k)).sort();
  return (
    <>
      <h3>Theme</h3>
      <p className="detail">
        Every device's, kept in the bridge's config. A row with "only this device" on is kept in this device's
        device.toml instead, over every device's.
      </p>
      {change.note && <p className="problem">{change.note}</p>}
      {theme === undefined ? (
        <p className="banner">Reading the theme…</p>
      ) : (
        <fieldset disabled={change.busy} className="theme">
          <ModeRow theme={theme} session={session} />
          {(["light", "dark"] as const).map((mode) => (
            <div key={mode}>
              <h4>{mode === "light" ? "Light" : "Dark"}</h4>
              {Object.entries(roles).map(([role, token]) => (
                <ColorRow
                  key={role}
                  label={role}
                  k={`${mode}.${role}`}
                  theme={theme}
                  fallback={builtIn(token, mode === "dark")}
                  session={session}
                />
              ))}
            </div>
          ))}
          <h4>Accounts</h4>
          {[...accounts].map(([k, label]) => {
            const network = theme.accounts.find((a) => `accounts.${a.on}` === k)?.network ?? "";
            return (
              <ColorRow
                key={k}
                label={label}
                k={k}
                theme={theme}
                fallback={builtIn(`--net-${networkMark(network)?.slug ?? "other"}`, dark)}
                session={session}
              />
            );
          })}
          <h4>People</h4>
          {people.length === 0 && <p className="detail">None picked: pick one from a message of theirs (Color).</p>}
          {people.map((k) => (
            <ColorRow
              key={k}
              label={k.slice("people.".length)}
              k={k}
              theme={theme}
              fallback={valueOf(theme, k)}
              session={session}
            />
          ))}
        </fieldset>
      )}
    </>
  );
}

/** WindowScale is what this window keeps alone, in Electron's store: its text size, and its zoom's keys. */
function WindowScale() {
  const [size, setSize] = useState<number | undefined>(undefined);
  const [note, setNote] = useState("");
  const failed = (err: unknown) => {
    setNote(`The text size could not be changed: ${err instanceof Error ? err.message : String(err)}`);
  };
  useEffect(() => {
    let live = true;
    window.kith.fontSize().then(
      (px) => {
        if (live) {
          setSize(px);
        }
      },
      (err: unknown) => {
        if (live) {
          failed(err);
        }
      },
    );
    return () => {
      live = false;
    };
  }, []);
  return (
    <>
      <h3>This window</h3>
      <p className="detail">
        Kept by this window alone, not in device.toml. ctrl+= and ctrl+- zoom it, ctrl+0 sets it back.
      </p>
      <label className="setting">
        Text size{" "}
        <select
          value={size ?? ""}
          disabled={size === undefined}
          onChange={(e) => {
            setNote("");
            window.kith.setFontSize(Number(e.target.value)).then(setSize, failed);
          }}
        >
          {fontSizes.map((px) => (
            <option key={px} value={px}>
              {`${String(px)} px${px === 15 ? " (the default)" : ""}`}
            </option>
          ))}
        </select>
      </label>
      {note && <p className="problem">{note}</p>}
    </>
  );
}

function Fields({ s, busy, session }: { s: DeviceSettings; busy: boolean; session: Session }) {
  const set = <K extends Key>(key: K, value: DeviceSettings[K]) => {
    void session.changeSetting(key, value);
  };
  const flag = (
    key:
      | "sendReceipts"
      | "unreadLine"
      | "markdown"
      | "confirmSms"
      | "sendTyping"
      | "checkBeforeSend"
      | "rareBeforeSend"
      | "complete"
      | "completeGhost",
  ) => (
    <label className="setting">
      <input
        type="checkbox"
        checked={s[key]}
        onChange={(e) => {
          set(key, e.target.checked);
        }}
      />{" "}
      {labels[key]}
    </label>
  );
  const word = (
    key: "spellUnderline" | "rareUnderline" | "autocorrect" | "completeScope",
    among: [string, string][],
  ) => (
    <label className="setting">
      {labels[key]}{" "}
      <select
        value={s[key]}
        onChange={(e) => {
          set(key, e.target.value);
        }}
      >
        {among.map(([value, what]) => (
          <option key={value} value={value}>
            {what ? `${value}: ${what}` : value}
          </option>
        ))}
      </select>
    </label>
  );
  return (
    <fieldset disabled={busy}>
      <h3>Reading</h3>
      {flag("sendReceipts")}
      <ReadRules
        rules={s.readRules}
        set={(rules) => {
          set("readRules", rules);
        }}
      />
      {flag("unreadLine")}
      <h3>Writing</h3>
      {flag("markdown")}
      {flag("confirmSms")}
      {flag("sendTyping")}
      <h3>Emoji</h3>
      <label className="setting">
        {labels.skinTone}{" "}
        <select
          value={s.skinTone}
          onChange={(e) => {
            set("skinTone", e.target.value);
          }}
        >
          {tones.map(([modifier, name]) => (
            <option key={name} value={modifier}>
              {`👍${modifier} ${name}`}
            </option>
          ))}
        </select>
      </label>
      <label className="setting">
        {labels.emojiSet}{" "}
        <select
          value={s.emojiSet}
          onChange={(e) => {
            set("emojiSet", e.target.value);
          }}
        >
          {emojiSets.map((name) => (
            <option key={name}>{name}</option>
          ))}
        </select>
      </label>
      <Shortcodes
        extra={s.emojiExtra}
        set={(extra) => {
          set("emojiExtra", extra);
        }}
      />
      <label className="setting">
        {labels.reactionsScope}{" "}
        <select
          value={s.reactionsScope}
          onChange={(e) => {
            set("reactionsScope", e.target.value);
          }}
        >
          {scopes.map(([scope, what]) => (
            <option key={scope} value={scope}>
              {`${scope}: ${what}`}
            </option>
          ))}
        </select>
      </label>
      <Palette
        list={s.reactionsStatic}
        set={(list) => {
          set("reactionsStatic", list);
        }}
      />
      <h3>Spelling</h3>
      <p className="detail">
        Checked by kithd, with the dictionaries and your own words the terminal client uses. Right-click a marked word,
        or press alt+S, to correct it.
      </p>
      {word("spellUnderline", spellUnderlines)}
      {word("rareUnderline", rareUnderlines)}
      {word("autocorrect", autocorrects)}
      {flag("checkBeforeSend")}
      {flag("rareBeforeSend")}
      <h3>Completion</h3>
      {flag("complete")}
      {flag("completeGhost")}
      {word("completeScope", completeScopes)}
    </fieldset>
  );
}

/** ReadRules lists [[display.read_rule]]: where receipts are announced otherwise. */
function ReadRules({ rules, set }: { rules: ReadRule[]; set: (rules: ReadRule[]) => void }) {
  const [match, setMatch] = useState("");
  const [sender, setSender] = useState("");
  const [send, setSend] = useState<Send>("private");
  const sendSelect = (value: Send, change: (send: Send) => void, label: string) => (
    <select
      aria-label={label}
      value={value}
      onChange={(e) => {
        change(e.target.value as Send);
      }}
    >
      <option value="inherit">inherit</option>
      <option value="announce">announce</option>
      <option value="private">private</option>
    </select>
  );
  return (
    <div className="setting">
      {labels.readRules}
      <span className="detail"> (the narrowest that names a room or a sender decides there)</span>
      <ul>
        {rules.map((r, i) => (
          <li key={i} className="setting-row">
            <span dir="auto">{r.match || "any room"}</span>
            <span dir="auto">{r.sender ? `from ${r.sender}` : "from anyone"}</span>
            {sendSelect(
              sendOf(r),
              (to) => {
                set(rules.map((rule, j) => (j === i ? { ...rule, send: sendFrom(to) } : rule)));
              },
              `Receipts for rule ${String(i + 1)}`,
            )}
            <button
              type="button"
              onClick={() => {
                set(rules.filter((_, j) => j !== i));
              }}
            >
              Remove
            </button>
          </li>
        ))}
      </ul>
      <form
        className="setting-row"
        onSubmit={(e) => {
          e.preventDefault();
          if (match.trim() === "" && sender.trim() === "") {
            return;
          }
          set([
            ...rules,
            { $typeName: "device.v1.ReadRule", match: match.trim(), sender: sender.trim(), send: sendFrom(send) },
          ]);
          setMatch("");
          setSender("");
        }}
      >
        <input
          aria-label="Room or place"
          placeholder="room ID, space:, tag: or place:"
          value={match}
          onChange={(e) => {
            setMatch(e.target.value);
          }}
        />
        <input
          aria-label="Sender"
          placeholder="sender ID (empty: anyone)"
          value={sender}
          onChange={(e) => {
            setSender(e.target.value);
          }}
        />
        {sendSelect(send, setSend, "Receipts for the new rule")}
        <button type="submit">Add rule</button>
      </form>
    </div>
  );
}

/** Shortcodes lists [display.emoji] extra: your own :shortcodes:. */
function Shortcodes({ extra, set }: { extra: Record<string, string>; set: (extra: Record<string, string>) => void }) {
  const [code, setCode] = useState("");
  const [emoji, setEmoji] = useState("");
  return (
    <div className="setting">
      {labels.emojiExtra}
      <ul>
        {Object.entries(extra).map(([name, glyph]) => (
          <li key={name} className="setting-row">
            <span>:{name}:</span>
            <span>{glyph}</span>
            <button
              type="button"
              onClick={() => {
                set(Object.fromEntries(Object.entries(extra).filter(([n]) => n !== name)));
              }}
            >
              Remove
            </button>
          </li>
        ))}
      </ul>
      <form
        className="setting-row"
        onSubmit={(e) => {
          e.preventDefault();
          const name = code.trim().replace(/^:|:$/g, "");
          if (name === "" || emoji.trim() === "") {
            return;
          }
          set({ ...extra, [name]: emoji.trim() });
          setCode("");
          setEmoji("");
        }}
      >
        <input
          aria-label="Shortcode"
          placeholder="shortcode"
          value={code}
          onChange={(e) => {
            setCode(e.target.value);
          }}
        />
        <input
          aria-label="Emoji"
          placeholder="emoji"
          value={emoji}
          onChange={(e) => {
            setEmoji(e.target.value);
          }}
        />
        <button type="submit">Add shortcode</button>
      </form>
    </div>
  );
}

/** Palette lists [display.reactions] static: the emoji the quick palette starts from. */
function Palette({ list, set }: { list: string[]; set: (list: string[]) => void }) {
  const [emoji, setEmoji] = useState("");
  return (
    <div className="setting">
      {labels.reactionsStatic}
      <span className="detail"> (none: the built-in ones)</span>
      <ul className="setting-row">
        {list.map((e, i) => (
          <li key={i}>
            {e}{" "}
            <button
              type="button"
              aria-label={`Remove ${e}`}
              onClick={() => {
                set(list.filter((_, j) => j !== i));
              }}
            >
              ×
            </button>
          </li>
        ))}
      </ul>
      <form
        className="setting-row"
        onSubmit={(e) => {
          e.preventDefault();
          if (emoji.trim() === "") {
            return;
          }
          set([...list, emoji.trim()]);
          setEmoji("");
        }}
      >
        <input
          aria-label="Reaction"
          placeholder="emoji"
          value={emoji}
          onChange={(e) => {
            setEmoji(e.target.value);
          }}
        />
        <button type="submit">Add</button>
      </form>
    </div>
  );
}
