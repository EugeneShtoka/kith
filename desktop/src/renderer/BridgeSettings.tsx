// The settings page's bridge settings: config.toml's, every device's, as kithd lists them
// (config.ts). A control per kind, each with its default, a reset and default.toml's
// text, as the TUI's settings screen shows them; each change set at once.
import { useState } from "react";

import { type ConfigSetting, ConfigSettingKind } from "../gen/backend/v1/services_pb";
import { bounds, flag, grouped, inForce, list, text, typed, words } from "./config";
import type { ConfigView, Session } from "./session";

export function BridgeSettings({ config, session }: { config: ConfigView; session: Session }) {
  return (
    <>
      <h3>Every device's settings</h3>
      <p className="detail">
        The bridge's own (config.toml), shared with the terminal client and every device. Programs, folders, addresses,
        accounts, keys, the log and what an assistant may reach are set in the terminal client or the file.
      </p>
      {config.note && <p className="problem">{config.note}</p>}
      {config.read === undefined ? (
        !config.note && <p className="banner">Reading the bridge's settings</p>
      ) : (
        <fieldset disabled={config.busy} className="bridge-settings">
          {grouped(config.read.settings).map((g) => (
            <section key={g.group} aria-label={g.title}>
              <h4>{g.title}</h4>
              {g.settings.map((s) => (
                <Row key={s.path} s={s} session={session} />
              ))}
            </section>
          ))}
        </fieldset>
      )}
    </>
  );
}

/** Row is one setting: its control, its default and reset, and what it does. */
function Row({ s, session }: { s: ConfigSetting; session: Session }) {
  const id = `config-${s.path}`;
  const range = bounds(s);
  return (
    <div className="config-setting">
      <Control id={id} s={s} session={session} />
      <div className="config-default">
        <span className="detail">
          {s.fallback === undefined ? "No default" : `Default: ${words(s.fallback)}`}
          {range && ` · ${range}`}
          {s.value === undefined && " · in force"}
        </span>
        {s.value !== undefined && (
          <button
            type="button"
            aria-label={`Reset ${s.label} to its default`}
            onClick={() => {
              void session.changeConfig(s.path, undefined);
            }}
          >
            Reset
          </button>
        )}
      </div>
      {s.doc && (
        <details>
          <summary>What it does</summary>
          <p className="config-doc">{s.doc}</p>
        </details>
      )}
    </div>
  );
}

function Control({ id, s, session }: { id: string; s: ConfigSetting; session: Session }) {
  const now = inForce(s);
  switch (s.kind) {
    case ConfigSettingKind.TOGGLE:
      return (
        <label className="setting" htmlFor={id}>
          <input
            id={id}
            type="checkbox"
            checked={now?.value.case === "flag" && now.value.value}
            onChange={(e) => {
              void session.changeConfig(s.path, flag(e.target.checked));
            }}
          />{" "}
          {s.label}
        </label>
      );
    case ConfigSettingKind.CHOICE: {
      const current = now?.value.case === "text" ? now.value.value : "";
      const choices = s.choices.some((c) => c.value === current)
        ? s.choices
        : [{ value: current, detail: "as the file has it" }, ...s.choices];
      return (
        <label className="setting" htmlFor={id}>
          {s.label}{" "}
          <select
            id={id}
            value={current}
            onChange={(e) => {
              void session.changeConfig(s.path, text(e.target.value));
            }}
          >
            {choices.map((c) => (
              <option key={c.value} value={c.value}>
                {c.detail ? `${c.value}: ${c.detail}` : c.value}
              </option>
            ))}
          </select>
        </label>
      );
    }
    case ConfigSettingKind.LIST:
      return <Entries id={id} s={s} session={session} />;
    default:
      return <Entry key={words(now)} id={id} s={s} session={session} />;
  }
}

/** Entry is a typed setting (a number, a length of time, text), set on Enter or Set. */
function Entry({ id, s, session }: { id: string; s: ConfigSetting; session: Session }) {
  const shown = s.value ?? s.fallback;
  const start = shown?.value.case === "number" || shown?.value.case === "text" ? String(shown.value.value) : "";
  const [entry, setEntry] = useState(start);
  const [problem, setProblem] = useState("");
  const hint =
    s.kind === ConfigSettingKind.DURATION
      ? "90s, 10m or 1h30m"
      : s.kind === ConfigSettingKind.NUMBER
        ? "a whole number"
        : "";
  return (
    <form
      className="setting-row"
      onSubmit={(e) => {
        e.preventDefault();
        const v = typed(s, entry);
        if (typeof v === "string") {
          setProblem(v);
          return;
        }
        setProblem("");
        void session.changeConfig(s.path, v);
      }}
    >
      <label htmlFor={id}>{s.label}</label>
      <input
        id={id}
        className="config-entry"
        inputMode={s.kind === ConfigSettingKind.NUMBER ? "numeric" : undefined}
        placeholder={hint}
        value={entry}
        onChange={(e) => {
          setEntry(e.target.value);
        }}
      />
      <button type="submit" disabled={entry === start}>
        Set
      </button>
      {problem && <span className="problem">{problem}</span>}
    </form>
  );
}

/** Entries is a list setting, set whole: an entry removed, or one added. */
function Entries({ id, s, session }: { id: string; s: ConfigSetting; session: Session }) {
  const now = inForce(s);
  const entries = now?.value.case === "list" ? now.value.value.entries : [];
  const [entry, setEntry] = useState("");
  return (
    <div className="setting">
      <span id={`${id}-label`}>{s.label}</span>
      <ul className="setting-row" aria-labelledby={`${id}-label`}>
        {entries.map((e, i) => (
          <li key={`${String(i)}-${e}`} className="config-entry-chip">
            <span dir="auto">{e}</span>{" "}
            <button
              type="button"
              aria-label={`Remove ${e}`}
              onClick={() => {
                void session.changeConfig(s.path, list(entries.filter((_, j) => j !== i)));
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
          if (entry.trim() === "") {
            return;
          }
          void session.changeConfig(s.path, list([...entries, entry.trim()]));
          setEntry("");
        }}
      >
        <input
          id={id}
          className="config-entry"
          aria-label={`Add to ${s.label}`}
          value={entry}
          onChange={(e) => {
            setEntry(e.target.value);
          }}
        />
        <button type="submit">Add</button>
      </form>
    </div>
  );
}
