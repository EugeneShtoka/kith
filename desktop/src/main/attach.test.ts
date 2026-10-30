import { existsSync } from "node:fs";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";

import { create, toBinary } from "@bufbuild/protobuf";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { DeviceService } from "../gen/device/v1/device_pb";
import { Attachments, uploadChunk } from "./attach";
import { type Frame, Gate } from "./gate";
import { FakeKithd, until } from "./testing/kithd";

const window: Frame = { url: "app://kith/index.html", parent: null };

let kithd: FakeKithd;
let attachments: Attachments;
let dir: string;

beforeEach(async () => {
  kithd = new FakeKithd();
  await kithd.start();
  const gate = new Gate(kithd.transport());
  await gate.call(
    window,
    DeviceService.method.device.name,
    toBinary(DeviceService.method.device.input, create(DeviceService.method.device.input)),
  );
  attachments = new Attachments((method, input, signal) => gate.own(method, input, signal));
  dir = await mkdtemp(path.join(tmpdir(), "kith-attach-"));
});

afterEach(async () => {
  await kithd.dispose();
  await rm(dir, { recursive: true, force: true });
});

/** file is a file on disk of n bytes, each its offset's low byte. */
async function file(name: string, n: number): Promise<{ path: string; bytes: Uint8Array }> {
  const bytes = Uint8Array.from({ length: n }, (_, i) => i % 251);
  const at = path.join(dir, name);
  await writeFile(at, bytes);
  return { path: at, bytes };
}

/** same: the bytes are alike (vitest's deep equality is slow over megabytes). */
function same(a: Uint8Array | undefined, b: Uint8Array): boolean {
  return a !== undefined && Buffer.from(a).equals(Buffer.from(b));
}

const calls = (method: string) => kithd.reached.filter((r) => r.startsWith(`${method}:`)).length;

describe("attachments", () => {
  it("bring a file to kithd in chunks, each cut chunk sent again where it was cut, and post it once", async () => {
    const f = await file("report.pdf", 2 * uploadChunk + 1234);
    const offered = await attachments.fromPath(f.path);
    expect(offered).toMatchObject({ name: "report.pdf", size: f.bytes.length });
    kithd.cutPuts = 2; // the first two chunks are cut off after kithd wrote them
    await attachments.send(offered.token, "!r:x", "the report");
    expect(kithd.posted).toHaveLength(1);
    expect(kithd.posted[0]).toMatchObject({ room: "!r:x", name: "report.pdf", caption: "the report" });
    expect(same(kithd.posted[0]?.data, f.bytes)).toBe(true);
    expect(calls("StartUpload")).toBe(1);
    expect(calls("PutUpload")).toBe(3 + 2);
    expect(calls("SendFile")).toBe(1);
    // Sent, the offer is used up: sending it again sends nothing.
    await expect(attachments.send(offered.token, "!r:x", "")).rejects.toThrow(/no longer offered/);
    expect(calls("StartUpload")).toBe(1);
  });

  it("post a file once though SendFile's answer is lost: the upload is used up, never sent again", async () => {
    const f = await file("photo.jpg", 10);
    const offered = await attachments.fromPath(f.path);
    kithd.loseSendFile = true;
    await expect(attachments.send(offered.token, "!r:x", "")).rejects.toThrow();
    expect(calls("SendFile")).toBe(1);
    expect(kithd.posted).toHaveLength(1);
  });

  it("stop an upload abandoned mid-file when the window goes: no chunk after, nothing posted", async () => {
    const f = await file("video.mp4", 3 * uploadChunk);
    const offered = await attachments.fromPath(f.path);
    kithd.holdPuts = true;
    const sending = attachments.send(offered.token, "!r:x", "");
    await until(() => kithd.heldPuts.length === 1, "the first chunk on its way");
    await attachments.abandon();
    kithd.holdPuts = false;
    kithd.heldPuts.splice(0).forEach((release) => {
      release();
    });
    await expect(sending).rejects.toThrow();
    expect(calls("PutUpload")).toBe(1);
    expect(calls("SendFile")).toBe(0);
    expect(kithd.posted).toHaveLength(0);
  });

  it("try a refused chunk no more: an upload kithd no longer has fails at once", async () => {
    const f = await file("notes.txt", 10);
    const offered = await attachments.fromPath(f.path);
    kithd.holdPuts = true;
    const sending = attachments.send(offered.token, "!r:x", "");
    await until(() => kithd.heldPuts.length === 1, "the chunk on its way");
    kithd.uploads.clear(); // expired at kithd meanwhile
    kithd.holdPuts = false;
    kithd.heldPuts.splice(0).forEach((release) => {
      release();
    });
    await expect(sending).rejects.toThrow(/no such upload/);
    expect(calls("PutUpload")).toBe(1);
  });

  it("send what the relay readied: its copy, under its name, the copy removed once sent", async () => {
    const f = await file("IMG_2041.jpg", 300);
    const copy = await mkdtemp(path.join(dir, "send-"));
    const clean = await file(path.join(path.basename(copy), "IMG_2041.jpg"), 200);
    kithd.outgoing = () => ({ path: clean.path, name: "photo.jpg", copied: true, kept: false });
    const offered = await attachments.fromPath(f.path);
    await attachments.send(offered.token, "!r:x", "");
    expect(kithd.posted[0]).toMatchObject({ name: "photo.jpg" });
    expect(kithd.posted[0]?.data).toEqual(clean.bytes);
    expect(existsSync(copy)).toBe(false);
    expect(existsSync(f.path)).toBe(true);
  });

  it("keep pasted bytes in a file of main's own until sent or taken back", async () => {
    const pasted = await attachments.fromBytes("../../Screenshot.png", new Uint8Array([1, 2, 3]));
    expect(pasted.name).toBe("Screenshot.png");
    const taken = await attachments.fromBytes("", new Uint8Array([4]));
    expect(taken.name).toBe("pasted");
    const made = () => kithd.received.length; // the relay is asked only as a file is sent
    expect(made()).toBe(1);
    await attachments.send(pasted.token, "!r:x", "");
    expect(kithd.posted[0]?.data).toEqual(new Uint8Array([1, 2, 3]));
    await attachments.discard(taken.token);
    await expect(attachments.send(taken.token, "!r:x", "")).rejects.toThrow(/no longer offered/);
  });
});
