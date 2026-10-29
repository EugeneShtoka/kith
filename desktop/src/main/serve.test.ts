import path from "node:path";

import { describe, expect, it } from "vitest";

import { appFile } from "./serve";

const root = path.resolve("/opt/kith/renderer");

describe("app://kith", () => {
  it("names the renderer's files", () => {
    expect(appFile(root, "app://kith/index.html")).toBe(path.join(root, "index.html"));
    expect(appFile(root, "app://kith/assets/main-1a2b.js?x=1")).toBe(path.join(root, "assets", "main-1a2b.js"));
    expect(appFile(root, "app://kith/a%20b.css")).toBe(path.join(root, "a b.css"));
  });

  it("names nothing on another host, or outside the renderer, however the path is spelled", () => {
    for (const url of ["app://evil/index.html", "app://kith.evil/index.html", "http://kith/index.html", "not a url"]) {
      expect(appFile(root, url), url).toBeUndefined();
    }
    // Every path spelled from these pieces: dot segments, plain and escaped, and
    // separators escaped. Whatever a URL decodes to, it stays under root or is nothing.
    const pieces = ["..", ".", "%2e%2e", "%2E.", "%2f", "%5c", "/", "a", "%00", "%"];
    let seed = 7;
    const random = () => (seed = (seed * 1103515245 + 12345) % 2 ** 31) / 2 ** 31;
    for (let i = 0; i < 2000; i++) {
      const spelled = Array.from(
        { length: 1 + Math.floor(random() * 8) },
        () => pieces[Math.floor(random() * pieces.length)],
      );
      const url = "app://kith/" + spelled.join("");
      const file = appFile(root, url);
      if (file !== undefined) {
        expect(file.startsWith(root + path.sep), `${url} -> ${file}`).toBe(true);
      }
    }
    expect(appFile(root, "app://kith/..%2f..%2fetc%2fpasswd")).toBe(path.join(root, "etc", "passwd"));
    expect(appFile(root, "app://kith/")).toBeUndefined();
  });
});
