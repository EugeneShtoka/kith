// A fake kithd for main's tests: BackendService and DeviceService on a unix socket, as
// kith-relay serves them, with every request it receives recorded.
import { mkdtempSync, rmSync } from "node:fs";
import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import type { Socket } from "node:net";
import { tmpdir } from "node:os";
import path from "node:path";

import {
  create,
  type DescMethodServerStreaming,
  fromBinary,
  type Message,
  type MessageInitShape,
  toBinary,
} from "@bufbuild/protobuf";
import { Code, ConnectError, type ConnectRouter, type Transport } from "@connectrpc/connect";
import { connectNodeAdapter, createConnectTransport } from "@connectrpc/connect-node";
import { encodeEnvelope } from "@connectrpc/connect/protocol";
import { endStreamFlag } from "@connectrpc/connect/protocol-connect";

import {
  BackendService,
  type Change,
  type LoginStep,
  LoginStepSchema,
  type MessagesRequest,
  RailRowSchema,
} from "../../gen/backend/v1/services_pb";
import { type Message as Row, RoomSchema } from "../../gen/backend/v1/types_pb";
import { DeviceService } from "../../gen/device/v1/device_pb";

/** One request kithd received: its method, profile and message. */
export interface Received {
  method: string;
  profile: string | null;
  message: Message;
}

/** BackendService's streams, by their path: each is served by hand, as kithd serves it. */
const streamsByPath = new Map<string, DescMethodServerStreaming>(
  BackendService.methods
    .filter((m): m is typeof m & DescMethodServerStreaming => m.methodKind === "server_streaming")
    .map((m) => [`/${BackendService.typeName}/${m.name}`, m]),
);

export class FakeKithd {
  readonly socket: string;
  readonly received: Received[] = [];
  /** Every request that reached kithd, as "Method:profile", any method, answered or not. */
  readonly reached: string[] = [];
  /** Changed streams opened, and how many have ended. */
  opened = 0;
  closed = 0;
  /** What each Changed stream sends as it opens; then it stays open until it ends. */
  burst: Change[] = [];
  /** Changed ends at once after its burst, like a kithd whose sync failed for good. */
  endAtOnce = false;
  /** What DeviceService answers: the config's profiles, and the one remembered. */
  profiles = ["Home"];
  remembered = "";
  /** The rooms CachedInvites answers with, by ID. */
  invited: string[] = [];
  /** Rooms JoinRoom answers not found for: gone since the invitation. */
  readonly gone = new Set<string>();
  /** What FetchEvent answers, by event: the message, or kithd's error; else not found. */
  readonly fetched = new Map<string, Row | ConnectError>();
  /**
   * The cache's numbering (its epoch) and every change in it, the message each wrote:
   * change n is changes[n-1]. A Messages stream starts after the change it asks for, in
   * this epoch, and says to read again for any other.
   */
  epoch = "e1";
  readonly changes: Row[] = [];
  /** What LoadImage answers, by event. */
  readonly pictures = new Map<string, Uint8Array>();
  /** Uploads staged (StartUpload), by ID: the name, the bytes, which bytes chunks covered. */
  readonly uploads = new Map<string, { name: string; data: Uint8Array; covered: Uint8Array }>();
  /** Files posted (SendFile), each using its upload up, posted or not. */
  readonly posted: { room: string; name: string; caption: string; data: Uint8Array }[] = [];
  /** PutUploads still to cut off: the chunk written, the answer lost on the way. */
  cutPuts = 0;
  /** SendFile posts, and its answer is lost. */
  loseSendFile = false;
  /** Holds PutUpload answers until released (each call waits on its own release). */
  readonly heldPuts: (() => void)[] = [];
  holdPuts = false;
  /** What the relay's Outgoing answers for a path: by default the file, as it is. */
  outgoing: (path: string) => { path: string; name: string; copied: boolean; kept: boolean } = (path) => ({
    path,
    name: "",
    copied: false,
    kept: false,
  });
  /**
   * Logins, as kithd runs them: each begun is named l1, l2… and answered with the next of
   * beginWith (else a QR step); an answer is the next of answerWith (else done). Those
   * canceled, by name; a BeginLogin answered only once releaseBegins is called, while
   * holdBegins is set.
   */
  beginWith: ((login: string) => LoginStep)[] = [];
  answerWith: ((login: string) => LoginStep)[] = [];
  readonly canceled: { login: string; profile: string }[] = [];
  holdBegins = false;
  private readonly heldBegins: (() => void)[] = [];
  private logins = 0;
  /** The spaces the rail's account rows are, and the spaces signed out of. */
  accountSpaces: string[] = [];
  readonly signedOut: string[] = [];
  /** Who you are (Selves), and the verification answers that reached kithd, as "Method txn:profile". */
  selves = ["matrix:@me:x"];
  readonly verifyAnswers: string[] = [];
  private verifications = 0;
  /** StartVerification answers once this settles. */
  verifyHeld: Promise<void> = Promise.resolve();
  private server: Server | undefined;
  private readonly sockets = new Set<Socket>();
  private readonly dir: string;
  private readonly open = new Map<string, Set<ServerResponse>>();

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
    this.sendOn(BackendService.method.changed, { change: c });
  }

  /** commit is the cache writing m: a change, sent on every open Messages stream. */
  commit(m: Row): void {
    this.changes.push(m);
    this.sendOn(BackendService.method.messages, { message: m, epoch: this.epoch, change: BigInt(this.changes.length) });
  }

  /** sendOn sends message on every open stream of method. */
  sendOn<M extends DescMethodServerStreaming>(method: M, message: MessageInitShape<M["output"]>): void {
    for (const res of this.open.get(method.name) ?? []) {
      res.write(frame(method, message));
    }
  }

  /** releaseBegins answers every BeginLogin held. */
  releaseBegins(): void {
    for (const release of this.heldBegins.splice(0)) {
      release();
    }
  }

  /** streams is how many streams of method are open now. */
  streams(method: DescMethodServerStreaming): number {
    return this.open.get(method.name)?.size ?? 0;
  }

  async start(): Promise<void> {
    const routes = (router: ConnectRouter) => {
      router.service(BackendService, {
        status: (req, ctx) => {
          this.received.push({ method: "Status", profile: ctx.requestHeader.get("Kith-Profile"), message: req });
          return { ready: true };
        },
        cachedInvites: () => ({ invites: this.invited.map((id) => create(RoomSchema, { id, membership: "invite" })) }),
        joinRoom: (req) => {
          if (this.gone.has(req.roomIdOrAlias)) {
            throw new ConnectError(`no room ${req.roomIdOrAlias}`, Code.NotFound);
          }
          return { roomId: req.roomIdOrAlias };
        },
        fetchEvent: (req) => {
          const found = this.fetched.get(req.eventId);
          if (found === undefined) {
            throw new ConnectError(`no such message ${req.eventId}`, Code.NotFound);
          }
          if (found instanceof ConnectError) {
            throw found;
          }
          return { message: found };
        },
        replaceDraft: (req, ctx) => {
          this.received.push({ method: "ReplaceDraft", profile: ctx.requestHeader.get("Kith-Profile"), message: req });
          return { saved: true, stored: req.draft };
        },
        startUpload: (req) => {
          const id = `up${String(this.uploads.size + this.posted.length + 1)}`;
          const size = Number(req.size);
          this.uploads.set(id, { name: req.name, data: new Uint8Array(size), covered: new Uint8Array(size) });
          return { uploadId: id };
        },
        putUpload: async (req) => {
          if (this.holdPuts) {
            await new Promise<void>((resolve) => this.heldPuts.push(resolve));
          }
          const up = this.uploads.get(req.uploadId);
          const at = Number(req.offset);
          if (up === undefined) {
            throw new ConnectError("no such upload", Code.NotFound);
          }
          if (at + req.data.length > up.data.length) {
            throw new ConnectError("past the upload's size", Code.InvalidArgument);
          }
          up.data.set(req.data, at);
          up.covered.fill(1, at, at + req.data.length);
          if (this.cutPuts > 0) {
            this.cutPuts--;
            throw new ConnectError("connection reset", Code.Unavailable);
          }
          return {};
        },
        sendFile: (req) => {
          const up = this.uploads.get(req.uploadId);
          if (up === undefined) {
            throw new ConnectError("no such upload", Code.NotFound);
          }
          if (up.covered.includes(0)) {
            throw new ConnectError("the upload is not complete", Code.FailedPrecondition);
          }
          this.uploads.delete(req.uploadId); // used up, sent or not
          this.posted.push({ room: req.roomId, name: req.name || up.name, caption: req.caption, data: up.data });
          if (this.loseSendFile) {
            throw new ConnectError("connection reset", Code.Unavailable);
          }
          return {};
        },
        leaveRoom: () => ({}),
        rail: () => ({
          rows: [
            create(RailRowSchema, { key: "Friends", label: "Friends", space: "!friends:x" }),
            ...this.accountSpaces.map((space) =>
              create(RailRowSchema, { key: space, label: space, space, home: "telegram", owner: space }),
            ),
          ],
        }),
        signOut: (req) => {
          this.signedOut.push(req.spaceId);
          return {};
        },
        selves: () => ({ ids: this.selves }),
        accounts: () => ({ accounts: [{ network: "Matrix", on: "matrix:1/", name: "@me:x" }] }),
        startVerification: async () => {
          await this.verifyHeld;
          return { txnId: "v" + String(++this.verifications) };
        },
        acceptVerification: (req, ctx) => {
          this.verifyAnswers.push(`AcceptVerification ${req.txnId}:${ctx.requestHeader.get("Kith-Profile") ?? ""}`);
          return {};
        },
        confirmSAS: (req, ctx) => {
          this.verifyAnswers.push(`ConfirmSAS ${req.txnId}:${ctx.requestHeader.get("Kith-Profile") ?? ""}`);
          return {};
        },
        cancelVerification: (req, ctx) => {
          this.verifyAnswers.push(`CancelVerification ${req.txnId}:${ctx.requestHeader.get("Kith-Profile") ?? ""}`);
          return {};
        },
        loginNetworks: () => ({ networks: [{ network: "whatsapp", label: "WhatsApp" }] }),
        beginLogin: async () => {
          if (this.holdBegins) {
            await new Promise<void>((resolve) => this.heldBegins.push(resolve));
          }
          const login = "l" + String(++this.logins);
          const step = this.beginWith.shift()?.(login) ?? create(LoginStepSchema, { login, qr: "2@x" });
          return { step };
        },
        answerLogin: (req) => ({
          step:
            this.answerWith.shift()?.(req.login) ?? create(LoginStepSchema, { login: req.login, done: "logged in" }),
        }),
        cancelLogin: (req, ctx) => {
          this.canceled.push({ login: req.login, profile: ctx.requestHeader.get("Kith-Profile") ?? "" });
          return {};
        },
        insteadLogin: () => ({}),
        loadImage: (req) => {
          const data = this.pictures.get(req.eventId);
          if (data === undefined) {
            throw new ConnectError(`no picture ${req.eventId}`, Code.NotFound);
          }
          return { data };
        },
      });
      router.service(DeviceService, {
        device: (req, ctx) => {
          this.received.push({ method: "Device", profile: ctx.requestHeader.get("Kith-Profile"), message: req });
          // As the relay: a remembered profile the config no longer has is none.
          const profile = this.profiles.includes(this.remembered) ? this.remembered : "";
          return { socket: this.socket, profile, profiles: this.profiles };
        },
        outgoing: (req) => this.outgoing(req.path),
        writeAccount: () => ({}),
        qRCode: () => ({ dark: new Uint8Array([1, 0, 0, 1]) }),
        chooseProfile: (req) => {
          const name = this.profiles.find((p) => p.toLowerCase() === req.name.toLowerCase());
          if (name === undefined) {
            throw new ConnectError(`no profile ${req.name}`, Code.InvalidArgument);
          }
          this.remembered = name;
          return { profile: name };
        },
        updateDeviceSettings: (req, ctx) => {
          this.received.push({
            method: "UpdateDeviceSettings",
            profile: ctx.requestHeader.get("Kith-Profile"),
            message: req,
          });
          return { settings: req.settings, revision: `${req.baseRevision}+1` };
        },
      });
    };
    const adapter = connectNodeAdapter({ routes });
    const server = createServer((req, res) => {
      const profile = req.headers["kith-profile"];
      this.reached.push(`${req.url?.split("/").pop() ?? ""}:${typeof profile === "string" ? profile : ""}`);
      const stream = streamsByPath.get(req.url ?? "");
      if (stream !== undefined) {
        void this.serve(stream, req, res);
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
   * serve serves a stream by hand, as kithd does: headers as it opens (a stream may be
   * silent for hours), then (Changed) the burst, then each frame sent until it ends.
   */
  private async serve(method: DescMethodServerStreaming, req: IncomingMessage, res: ServerResponse): Promise<void> {
    const body: Buffer[] = [];
    for await (const chunk of req) {
      body.push(chunk as Buffer);
    }
    const message = fromBinary(method.input, Buffer.concat(body).subarray(5));
    const profile = req.headers["kith-profile"];
    this.received.push({ method: method.name, profile: typeof profile === "string" ? profile : null, message });
    const changed = method.name === BackendService.method.changed.name;
    if (changed) {
      this.opened++;
    }
    res.writeHead(200, { "Content-Type": "application/connect+proto" });
    res.flushHeaders();
    if (changed) {
      for (const c of this.burst) {
        res.write(frame(BackendService.method.changed, { change: c }));
      }
    }
    if (method.name === BackendService.method.messages.name) {
      replay(res, message as MessagesRequest, this.epoch, this.changes);
    }
    const open = this.open.get(method.name) ?? new Set();
    this.open.set(method.name, open);
    res.on("close", () => {
      open.delete(res);
      if (changed) {
        this.closed++;
      }
    });
    if (changed && this.endAtOnce) {
      res.end(encodeEnvelope(endStreamFlag, new TextEncoder().encode("{}")));
      return;
    }
    open.add(res);
  }
}

/**
 * replay starts a Messages stream as kithd does: a frame saying where it starts, the
 * changes after since sent again; or, since being another epoch's or a number never
 * given, a frame saying to read again, then from the newest.
 */
function replay(res: ServerResponse, since: MessagesRequest, epoch: string, changes: readonly Row[]): void {
  const method = BackendService.method.messages;
  const newest = BigInt(changes.length);
  const resumes = since.sinceEpoch === epoch && since.sinceChange <= newest;
  const start = resumes ? since.sinceChange : newest;
  const readAgain = since.sinceEpoch !== "" && !resumes;
  res.write(frame(method, { epoch, change: start, readAgain }));
  for (let n = start; n < newest; n++) {
    res.write(frame(method, { message: changes[Number(n)], epoch, change: n + 1n }));
  }
}

function frame<M extends DescMethodServerStreaming>(method: M, message: MessageInitShape<M["output"]>): Uint8Array {
  return encodeEnvelope(0, toBinary(method.output, create(method.output, message)));
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
