import { randomUUID } from "node:crypto";
import { mkdtemp, open, rm, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";

import type { DescMessage, DescMethodUnary, MessageInitShape, MessageShape } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";

import { BackendService } from "../gen/backend/v1/services_pb";
import { DeviceService } from "../gen/device/v1/device_pb";
import type { Offered } from "../shared/ipc";

const backend = BackendService.method;

/** How much of a file one PutUpload carries: all main holds of it at once (as daemon.Remote). */
export const uploadChunk = 1 << 20;

/** How often one chunk is tried, and the wait before the next try, longer each time. */
export const chunkTries = 4;
export const chunkPause = 250;

/** A call main makes for the window, in its profile (Gate.own). */
export type Call = <I extends DescMessage, O extends DescMessage>(
  method: DescMethodUnary<I, O>,
  input: MessageInitShape<I>,
  signal: AbortSignal,
) => Promise<MessageShape<O>>;

/** A file the person offered: where it is, and a folder main made for it (pasted), if any. */
interface Offer {
  file: string;
  name: string;
  size: number;
  made: string | undefined;
}

/**
 * Attachments are the files the person offered the window to send: picked in the open
 * dialog, dropped, or pasted. The window names none by path: main reads a file from the
 * path the dialog answered or the system gave the dropped file (the preload's
 * webUtils.getPathForFile), or writes the bytes pasted to a file of its own, and hands
 * the window a token for it. Sending one readies it as the TUI does (the relay's
 * Outgoing: anonymized where [display.media] says), brings its bytes to kithd in chunks
 * (StartUpload, PutUpload, each chunk tried again where it was cut off), then posts it
 * (SendFile), as daemon.Remote does.
 */
export class Attachments {
  private readonly offers = new Map<string, Offer>();
  private stopped = new AbortController();

  constructor(private readonly call: Call) {}

  /** fromPath offers the file at path, which the dialog answered or the system gave a drop. */
  async fromPath(file: string): Promise<Offered> {
    const info = await stat(file);
    if (!info.isFile()) {
      throw new Error(`kith: ${path.basename(file)} is not a file`);
    }
    return this.offer({ file, name: path.basename(file), size: info.size, made: undefined });
  }

  /** fromBytes offers bytes pasted into the window, under the name they came with. */
  async fromBytes(name: string, bytes: Uint8Array): Promise<Offered> {
    const base = path.basename(name.replaceAll("\\", "/"));
    const named = base === "" || base === "." || base === ".." ? "pasted" : base;
    const made = await mkdtemp(path.join(tmpdir(), "kith-pasted-"));
    const file = path.join(made, named);
    await writeFile(file, bytes, { mode: 0o600 });
    return this.offer({ file, name: named, size: bytes.length, made });
  }

  /** discard forgets an offer the person took back. */
  async discard(token: string): Promise<void> {
    const offer = this.offers.get(token);
    this.offers.delete(token);
    await removeMade(offer);
  }

  /**
   * send posts an offered file to room with caption; kept says it went with metadata kith
   * cannot remove. It is sent once: a SendFile whose answer is lost is not sent again
   * (kithd has used the upload up, sent or not), so a file is never posted twice.
   */
  async send(token: string, room: string, caption: string): Promise<{ kept: boolean }> {
    const offer = this.offers.get(token);
    if (offer === undefined) {
      throw new Error("kith: that file is no longer offered");
    }
    this.offers.delete(token);
    const signal = this.stopped.signal;
    try {
      const ready = await this.call(DeviceService.method.outgoing, { path: offer.file }, signal);
      try {
        await this.upload(ready.path, offer, room, caption, ready.name, signal);
      } finally {
        if (ready.copied) {
          await rm(path.dirname(ready.path), { recursive: true, force: true });
        }
      }
      return { kept: ready.kept };
    } finally {
      await removeMade(offer);
    }
  }

  /**
   * abandon stops every send in flight and forgets every offer: the window closed or
   * loaded afresh, and nobody waits for them. What kithd staged of a cut upload it
   * removes once no chunk reaches it for an hour (daemon.uploadIdle).
   */
  async abandon(): Promise<void> {
    this.stopped.abort(new Error("kith: the window went away"));
    this.stopped = new AbortController();
    const offers = [...this.offers.values()];
    this.offers.clear();
    await Promise.all(offers.map(removeMade));
  }

  private offer(o: Offer): Offered {
    const token = randomUUID();
    this.offers.set(token, o);
    return { token, name: o.name, size: o.size };
  }

  private async upload(
    file: string,
    offer: Offer,
    room: string,
    caption: string,
    name: string,
    signal: AbortSignal,
  ): Promise<void> {
    const handle = await open(file, "r");
    try {
      const size = (await handle.stat()).size;
      const started = await this.call(backend.startUpload, { name: offer.name, size: BigInt(size) }, signal);
      for (let offset = 0; offset < size; offset += uploadChunk) {
        const chunk = new Uint8Array(Math.min(uploadChunk, size - offset));
        const { bytesRead } = await handle.read(chunk, 0, chunk.length, offset);
        if (bytesRead !== chunk.length) {
          throw new Error(`kith: ${offer.name} changed while it was sent`);
        }
        await this.put(started.uploadId, offset, chunk, signal);
      }
      signal.throwIfAborted();
      await this.call(backend.sendFile, { roomId: room, uploadId: started.uploadId, caption, name }, signal);
    } finally {
      await handle.close();
    }
  }

  /** put writes one chunk, again after a failure that may pass, not after a refusal. */
  private async put(uploadId: string, offset: number, data: Uint8Array, signal: AbortSignal): Promise<void> {
    for (let attempt = 1; ; attempt++) {
      try {
        await this.call(backend.putUpload, { uploadId, offset: BigInt(offset), data }, signal);
        return;
      } catch (err) {
        if (attempt >= chunkTries || signal.aborted || refused(err)) {
          throw err;
        }
      }
      await new Promise((resolve) => setTimeout(resolve, attempt * chunkPause));
    }
  }
}

/** removeMade removes the folder main made for a pasted offer. */
async function removeMade(offer: Offer | undefined): Promise<void> {
  if (offer?.made !== undefined) {
    await rm(offer.made, { recursive: true, force: true });
  }
}

/** refused: kithd turned the call down, which asking again will not change (daemon.refused). */
function refused(err: unknown): boolean {
  const code = ConnectError.from(err).code;
  return [
    Code.NotFound,
    Code.InvalidArgument,
    Code.FailedPrecondition,
    Code.ResourceExhausted,
    Code.Unimplemented,
    Code.Canceled,
    Code.DeadlineExceeded,
  ].includes(code);
}
