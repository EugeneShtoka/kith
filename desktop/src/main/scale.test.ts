import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";

import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { defaultScale, ScaleStore, valid, zoomed, zoomKey, zooms } from "./scale";

let dir: string;
let file: string;

beforeEach(async () => {
  dir = await mkdtemp(path.join(tmpdir(), "kith-scale-"));
  file = path.join(dir, "scale.json");
});

afterEach(async () => {
  await rm(dir, { recursive: true, force: true });
});

const key = (k: string, mods: { control?: boolean; meta?: boolean; alt?: boolean } = { control: true }) => ({
  type: "keyDown",
  key: k,
  control: mods.control ?? false,
  meta: mods.meta ?? false,
  alt: mods.alt ?? false,
});

describe("the window's scale", () => {
  it("zooms with ctrl or cmd and =, +, - or 0, and with nothing else", () => {
    expect(zoomKey(key("="))).toBe("in");
    expect(zoomKey(key("+"))).toBe("in");
    expect(zoomKey(key("-", { meta: true }))).toBe("out");
    expect(zoomKey(key("0"))).toBe("reset");
    expect(zoomKey(key("=", {}))).toBeUndefined();
    expect(zoomKey(key("=", { control: true, alt: true }))).toBeUndefined();
    expect(zoomKey(key("k"))).toBeUndefined();
    expect(zoomKey({ ...key("="), type: "keyUp" })).toBeUndefined();
  });

  it("steps through the zooms, held at either end", () => {
    expect(zoomed(1, "in")).toBe(1.1);
    expect(zoomed(1, "out")).toBe(0.9);
    expect(zoomed(1.5, "reset")).toBe(1);
    expect(zoomed(zooms[zooms.length - 1] ?? 0, "in")).toBe(zooms[zooms.length - 1]);
    expect(zoomed(zooms[0], "out")).toBe(zooms[0]);
    // A zoom not on the steps (set elsewhere) moves from 100%.
    expect(zoomed(1.33, "in")).toBe(1.1);
  });

  it("keeps only what the window offers, each key apart", () => {
    expect(valid({ zoom: 1.25, fontSize: 18 })).toEqual({ zoom: 1.25, fontSize: 18 });
    expect(valid({ zoom: 7, fontSize: 18 })).toEqual({ zoom: 1, fontSize: 18 });
    expect(valid({ zoom: 1.25, fontSize: "18" })).toEqual({ zoom: 1.25, fontSize: 15 });
    expect(valid(null)).toEqual(defaultScale);
    expect(valid([])).toEqual(defaultScale);
  });

  it("starts from the defaults where the store is missing or unreadable", async () => {
    expect(await new ScaleStore(file).load()).toEqual(defaultScale);
    await writeFile(file, "{ not json");
    expect(await new ScaleStore(file).load()).toEqual(defaultScale);
  });

  it("writes each change in order, the last one left, and reads it back", async () => {
    const store = new ScaleStore(file);
    await store.load();
    const writes = [store.set({ zoom: 1.1 }), store.set({ fontSize: 18 }), store.set({ zoom: 1.25 })];
    expect(store.get()).toEqual({ zoom: 1.25, fontSize: 18 });
    await Promise.all(writes);
    expect(JSON.parse(await readFile(file, "utf8"))).toEqual({ zoom: 1.25, fontSize: 18 });
    expect(await new ScaleStore(file).load()).toEqual({ zoom: 1.25, fontSize: 18 });
  });

  it("goes on writing after a write fails", async () => {
    const sub = path.join(dir, "sub");
    const store = new ScaleStore(path.join(sub, "scale.json"));
    await expect(store.set({ zoom: 1.1 })).rejects.toThrow();
    await mkdir(sub);
    await store.set({ fontSize: 13 });
    expect(await new ScaleStore(path.join(sub, "scale.json")).load()).toEqual({ zoom: 1.1, fontSize: 13 });
  });
});
