// A fake kithd for main's tests: BackendService and DeviceService on a unix socket, as
// kith-relay serves them, with every request it receives recorded.
import { mkdtempSync, rmSync } from "node:fs";
import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import type { Socket } from "node:net";
import { tmpdir } from "node:os";
import path from "node:path";

import { create, fromBinary, type Message, toBinary } from "@bufbuild/protobuf";
import type { ConnectRouter, Transport } from "@connectrpc/connect";
import { connectNodeAdapter, createConnectTransport } from "@connectrpc/connect-node";
import { encodeEnvelope } from "@connectrpc/connect/protocol";
import { endStreamFlag } from "@connectrpc/connect/protocol-connect";

import {
  BackendService,
  type Change,
  ChangedRequestSchema,
  ChangedResponseSchema,
} from "../../gen/backend/v1/services_pb";
import { DeviceService } from "../../gen/device/v1/device_pb";

/** One request kithd received: its method, profile and message. */
export interface Received {
  method: string;
  profile: string | null;
  message: Message;
}

const changedPath = `/${BackendService.typeName}/${BackendService.method.changed.name}`;

export class FakeKithd {
  readonly socket: string;
  readonly received: Received[] = [];
  /** Changed streams opened, and how many have ended. */
  opened = 0;
  closed = 0;
  /** What each Changed stream sends as it opens; then it stays open until it ends. */
  burst: Change[] = [];
  /** Changed ends at once after its burst, like a kithd whose sync failed for good. */
  endAtOnce = false;
  private server: Server | undefined;
  private readonly sockets = new Set<Socket>();
  private readonly dir: string;
  private readonly open = new Set<ServerResponse>();

  constructor() {
    this.dir = mkdtempSync(path.join(tmpdir(), "kith-"));
    this.socket = path.join(this.dir, "kithd.sock");
  }

  /** transport reaches this kithd as main reaches the relay. */
  transport(): Transport {
    return createConnectTransport({
      httpVersion: "1.1",
      baseUrl: "http://kithd",
      nodeOptions: { socketPath: this.socket },
    });
  }

  /** send sends c on every open Changed stream. */
  send(c: Change): void {
    for (const res of this.open) {
      res.write(frame(c));
    }
  }

  async start(): Promise<void> {
    const routes = (router: ConnectRouter) => {
      router.service(BackendService, {
        status: (req, ctx) => {
          this.received.push({ method: "Status", profile: ctx.requestHeader.get("Kith-Profile"), message: req });
          return { ready: true };
        },
      });
      router.service(DeviceService, {
        device: (req, ctx) => {
          this.received.push({ method: "Device", profile: ctx.requestHeader.get("Kith-Profile"), message: req });
          return { socket: this.socket, profile: "Home" };
        },
      });
    };
    const adapter = connectNodeAdapter({ routes });
    const server = createServer((req, res) => {
      if (req.url === changedPath) {
        void this.changed(req, res);
      } else {
        adapter(req, res);
      }
    });
    server.on("connection", (s: Socket) => {
      this.sockets.add(s);
      s.on("close", () => this.sockets.delete(s));
    });
    await new Promise<void>((resolve) => {
      server.listen(this.socket, resolve);
    });
    this.server = server;
  }

  /** stop is kithd exiting: every connection cut, streams mid-frame included. */
  async stop(): Promise<void> {
    const server = this.server;
    this.server = undefined;
    if (server === undefined) {
      return;
    }
    for (const s of this.sockets) {
      s.destroy();
    }
    await new Promise<void>((resolve) => {
      server.close(() => {
        resolve();
      });
    });
  }

  async dispose(): Promise<void> {
    await this.stop();
    rmSync(this.dir, { recursive: true, force: true });
  }

  /**
   * changed serves Changed by hand, as kithd does: headers as it opens (a stream may be
   * silent for hours), then the burst, then each frame sent until the stream ends.
   */
  private async changed(req: IncomingMessage, res: ServerResponse): Promise<void> {
    const body: Buffer[] = [];
    for await (const chunk of req) {
      body.push(chunk as Buffer);
    }
    const message = fromBinary(ChangedRequestSchema, Buffer.concat(body).subarray(5));
    const profile = req.headers["kith-profile"];
    this.received.push({ method: "Changed", profile: typeof profile === "string" ? profile : null, message });
    this.opened++;
    res.writeHead(200, { "Content-Type": "application/connect+proto" });
    res.flushHeaders();
    for (const c of this.burst) {
      res.write(frame(c));
    }
    res.on("close", () => {
      this.open.delete(res);
      this.closed++;
    });
    if (this.endAtOnce) {
      res.end(encodeEnvelope(endStreamFlag, new TextEncoder().encode("{}")));
      return;
    }
    this.open.add(res);
  }
}

function frame(c: Change): Uint8Array {
  return encodeEnvelope(0, toBinary(ChangedResponseSchema, create(ChangedResponseSchema, { change: c })));
}

/** until waits for cond, failing after ms. */
export async function until(cond: () => boolean, what: string, ms = 5000): Promise<void> {
  const deadline = Date.now() + ms;
  while (!cond()) {
    if (Date.now() > deadline) {
      throw new Error(`timed out waiting for ${what}`);
    }
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
}
