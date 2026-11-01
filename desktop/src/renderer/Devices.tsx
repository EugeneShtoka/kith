// The settings page's devices: the devices paired with this kithd, pairing another by QR
// code as `kith pair` does, unpairing one, and joining a bridge from here as `kith join`
// does (devices.ts). Confirm, Unpair and Join each have main ask again in a dialog of its
// own, which alone sends them.
import { useState } from "react";

import type { Device } from "../gen/backend/v1/services_pb";
import { type DevicesView, type JoinView, notListening, type PairingView } from "./devices";
import { QRCode } from "./Accounts";
import type { Session } from "./session";

/** when is a device's pairing date, as the page shows it. */
function when(d: Device): string {
  return d.addedAt === undefined ? "" : new Date(Number(d.addedAt.seconds) * 1000).toLocaleDateString();
}

export function Devices({ devices, bridge, session }: { devices: DevicesView; bridge: string; session: Session }) {
  const [name, setName] = useState("");
  return (
    <>
      <h3>Devices</h3>
      <p className="detail">
        A device paired with this kithd may do everything this window may: read and send on every account, change the
        config, pair and unpair devices. Pair only your own.
      </p>
      {devices.note && <p className="banner">{devices.note}</p>}
      <dl className="device-self">
        <dt>{bridge === "" ? "This kithd" : `The bridge ${bridge}`}</dt>
        <dd className="device-id">{devices.self}</dd>
      </dl>
      {devices.list === undefined ? (
        <p className="banner">Reading the devices</p>
      ) : devices.list.length === 0 ? (
        <p className="detail">No device is paired yet.</p>
      ) : (
        <ul className="devices">
          {devices.list.map((d) => (
            <li key={d.id} className="device">
              <span className="device-name" dir="auto">
                {d.name || "unnamed"}
              </span>
              <span className="detail">
                {d.role === "bridge" ? "bridge" : "device"}
                {when(d) && `, paired ${when(d)}`}
              </span>
              <span className="device-id">{d.id}</span>
              <button
                type="button"
                disabled={devices.unpairing !== ""}
                onClick={() => {
                  session.unpairDevice(d);
                }}
              >
                {devices.unpairing === d.id ? "Unpairing" : "Unpair"}
              </button>
            </li>
          ))}
        </ul>
      )}
      <form
        className="pair-start"
        onSubmit={(e) => {
          e.preventDefault();
          session.pairDevice(name);
        }}
      >
        <label>
          <span>
            List it as <span className="detail">(optional: the name it gives)</span>
          </span>
          <input
            value={name}
            spellCheck={false}
            onChange={(e) => {
              setName(e.target.value);
            }}
          />
        </label>
        <button type="submit" disabled={devices.pairing !== undefined}>
          Pair a device
        </button>
      </form>
      <Join join={devices.join} session={session} />
      {devices.pairing && <PairingDialog p={devices.pairing} session={session} />}
    </>
  );
}

/** PairingDialog is a pairing under way, over the page: the code, then the device that answered. */
function PairingDialog({ p, session }: { p: PairingView; session: Session }) {
  const close = (label: string) => (
    <button
      type="button"
      onClick={() => {
        session.closePairing();
      }}
    >
      {label}
    </button>
  );
  return (
    <div className="verify-backdrop">
      <section className="verify pair" role="dialog" aria-modal="true" aria-label="Pair a device">
        <h2>Pair a device</h2>
        {p.ended !== "" ? (
          <>
            <Ended p={p} />
            <p className="verify-actions">{close("Close")}</p>
          </>
        ) : p.pending !== undefined ? (
          <>
            <p>
              <strong dir="auto">{p.pending.name || "A device"}</strong> asks to pair
              {p.pending.role === "bridge"
                ? " as a bridge: it joins the mesh, and every bridge admits its devices"
                : ""}
              .
            </p>
            <p className="device-id">{p.pending.id}</p>
            <p>It shows</p>
            <p className="pair-digits">{p.pending.digits}</p>
            <p className="detail">
              Confirm only if the device asking shows the same six digits. kith asks once more before it pairs it.
            </p>
            {p.note && <p className="problem">{p.note}</p>}
            <p className="verify-actions">
              <button
                type="button"
                disabled={p.confirming}
                onClick={() => {
                  session.refusePairing();
                }}
              >
                Refuse
              </button>
              <button
                type="button"
                className="primary"
                disabled={p.confirming}
                onClick={() => {
                  session.confirmPairing();
                }}
              >
                {p.confirming ? "Confirming" : "Confirm"}
              </button>
            </p>
          </>
        ) : (
          <>
            <p>
              On the new device, scan this code, or paste the line below into <code>kith join</code> or the
              window&apos;s Join a bridge.
            </p>
            {p.qr ? <QRCode qr={p.qr} /> : <p className="banner">Making the code</p>}
            {p.uri !== "" && <Copyable text={p.uri} />}
            <p className="detail">
              Good once
              {p.expires && `, until ${p.expires.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}`}.
              Waiting for the device. Closing this ends the pairing.
            </p>
            {p.note && <p className="problem">{p.note}</p>}
            <p className="verify-actions">{close("Cancel")}</p>
          </>
        )}
      </section>
    </div>
  );
}

/** Ended says how a pairing ended. */
function Ended({ p }: { p: PairingView }) {
  switch (p.ended) {
    case "paired":
      return <p>Paired. It may now do everything this window may.</p>;
    case "refused":
      return <p>Refused: nothing was paired.</p>;
    case "expired":
      return <p>Ten minutes passed unconfirmed: nothing was paired. Pair again for a new code.</p>;
    case "abandoned":
      return <p>The other device went away before it was confirmed: nothing was paired.</p>;
    default:
      return notListening(p.note) ? (
        <>
          <p className="problem">This kithd listens for no other device, so there is nothing to pair with.</p>
          <p>
            Set <code>[remote] listen</code> in its <code>config.toml</code> to the addresses your devices reach it at,
            such as <code>listen = [&quot;192.168.1.5:7443&quot;]</code>, then pair again.
          </p>
        </>
      ) : (
        <p className="problem">Not paired: {p.note}</p>
      );
  }
}

/** Copyable is text to copy whole: selected as it is clicked, for the system's copy. */
function Copyable({ text }: { text: string }) {
  return (
    <textarea
      className="copyable"
      readOnly
      value={text}
      rows={4}
      spellCheck={false}
      aria-label="The pairing's line, to copy"
      onFocus={(e) => {
        e.currentTarget.select();
      }}
    />
  );
}

/** Join is joining a bridge from this device: the line pasted, then the digits to compare. */
function Join({ join, session }: { join: JoinView | undefined; session: Session }) {
  const [uri, setURI] = useState("");
  const [name, setName] = useState("");
  return (
    <>
      <h4>Join a bridge</h4>
      <p className="detail">
        Use another kithd from this device: paste the line its <code>kith pair</code> or its window&apos;s Pair a device
        shows. Once joined, this window, the terminal client and kith-notify here reach kithd there.
      </p>
      {join === undefined || join.stage === "failed" ? (
        <form
          className="join"
          onSubmit={(e) => {
            e.preventDefault();
            session.joinBridge(uri, name);
          }}
        >
          {join?.stage === "failed" && <p className="problem">Not joined: {join.note}</p>}
          <label>
            The pairing&apos;s line
            <textarea
              value={uri}
              rows={3}
              spellCheck={false}
              placeholder="kith://pair?addr=…"
              onChange={(e) => {
                setURI(e.target.value);
              }}
            />
          </label>
          <label>
            <span>
              List this device as <span className="detail">(optional: this machine&apos;s name)</span>
            </span>
            <input
              value={name}
              spellCheck={false}
              onChange={(e) => {
                setName(e.target.value);
              }}
            />
          </label>
          <p className="login-actions">
            <button type="submit" disabled={uri.trim() === ""}>
              Join
            </button>
          </p>
        </form>
      ) : (
        <section className="login join-run" aria-label="Joining a bridge">
          {join.stage === "asking" ? (
            <p className="banner">Joining: confirm the bridge in kith&apos;s own dialog</p>
          ) : join.stage === "digits" ? (
            <>
              <p>This device shows</p>
              <p className="pair-digits">{join.digits}</p>
              <p className="detail">Confirm on the other device that it shows the same.</p>
            </>
          ) : (
            <p>Joined {join.bridge}. The window reconnects through it.</p>
          )}
          {join.stage !== "joined" && (
            <p className="login-actions">
              <button
                type="button"
                onClick={() => {
                  session.dismissJoin();
                }}
              >
                Cancel
              </button>
            </p>
          )}
        </section>
      )}
    </>
  );
}
