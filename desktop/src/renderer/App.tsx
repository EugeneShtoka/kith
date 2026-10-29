import { timestampDate } from "@bufbuild/protobuf/wkt";
import { useEffect, useState } from "react";

import {
  BackendService,
  Change,
  NetworkPhase,
  type NetworkStatus,
  type StatusResponse,
} from "../gen/backend/v1/services_pb";
import { type DeviceResponse, DeviceService } from "../gen/device/v1/device_pb";
import { call, subscribe } from "./kith";

/** Where the window's link to kithd is: its Changed stream, held open by main. */
type Link = { state: "connecting" } | { state: "attached" } | { state: "detached"; error: string };

const phases: Record<NetworkPhase, string> = {
  [NetworkPhase.UNSPECIFIED]: "unknown",
  [NetworkPhase.LOGGED_OUT]: "logged out",
  [NetworkPhase.CONNECTING]: "connecting",
  [NetworkPhase.ONLINE]: "online",
  [NetworkPhase.FAILED]: "failed",
};

const when = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "medium" });

export function App() {
  const [device, setDevice] = useState<DeviceResponse>();
  const [link, setLink] = useState<Link>({ state: "connecting" });
  const [status, setStatus] = useState<StatusResponse>();
  const [problem, setProblem] = useState<string>();

  useEffect(() => {
    const failed = (err: unknown) => {
      setProblem(err instanceof Error ? err.message : String(err));
    };
    // Status is read when the link opens, when an account changes, and when the link is
    // lost: a kithd waiting on someone answers Status and nothing else.
    const readStatus = () => {
      call(BackendService.method.status, {}).then((s) => {
        setStatus(s);
        setProblem(undefined);
      }, failed);
    };
    call(DeviceService.method.device, {}).then(setDevice, failed);
    return subscribe(BackendService.method.changed, {}, (event) => {
      switch (event.kind) {
        case "attached":
          setLink({ state: "attached" });
          readStatus();
          break;
        case "frames":
          if (event.frames.some((f) => f.change === Change.ACCOUNTS)) {
            readStatus();
          }
          break;
        case "detached":
          setLink({ state: "detached", error: event.error });
          readStatus();
          break;
        case "refused":
          failed(new Error(event.error));
          break;
      }
    });
  }, []);

  return (
    <main>
      <header>
        <h1>kith</h1>
        {device && <span className="profile">{device.profile}</span>}
      </header>
      <Banner link={link} status={status} />
      {problem && <p className="problem">{problem}</p>}
      {status && <Status status={status} />}
      {device && (
        <footer>
          kithd at <code>{device.socket}</code>, through kith-relay
        </footer>
      )}
    </main>
  );
}

function Banner({ link, status }: { link: Link; status: StatusResponse | undefined }) {
  if (status?.waiting) {
    return <p className="banner waiting">kithd is waiting: {status.waiting}</p>;
  }
  switch (link.state) {
    case "connecting":
      return <p className="banner">Connecting to kithd…</p>;
    case "detached":
      return <p className="banner lost">kithd is not reachable ({link.error}). Trying again.</p>;
    case "attached":
      return <p className="banner ok">Connected</p>;
  }
}

function Status({ status }: { status: StatusResponse }) {
  return (
    <section>
      <dl>
        <dt>Cache</dt>
        <dd>{status.ready ? "synced" : "not synced yet"}</dd>
        <dt>Last sync</dt>
        <dd>{status.syncedAt ? when.format(timestampDate(status.syncedAt)) : "never"}</dd>
        {status.lastError && (
          <>
            <dt>Last error</dt>
            <dd>{status.lastError}</dd>
          </>
        )}
      </dl>
      <table>
        <thead>
          <tr>
            <th>Network</th>
            <th>Account</th>
            <th>State</th>
          </tr>
        </thead>
        <tbody>
          {status.networks.map((n) => (
            <Network key={`${n.network}/${n.account}`} network={n} />
          ))}
        </tbody>
      </table>
    </section>
  );
}

function Network({ network }: { network: NetworkStatus }) {
  return (
    <tr>
      <td>{network.network}</td>
      <td dir="auto">{network.account}</td>
      <td>
        {phases[network.phase]}
        {network.detail && <span className="detail">: {network.detail}</span>}
      </td>
    </tr>
  );
}
