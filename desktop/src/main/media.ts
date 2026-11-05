import { mediaOf, pictureOf } from "../shared/media";

/**
 * byteRange is the first and last byte a Range header asks of size bytes: undefined for
 * a header it does not read (several ranges, another unit), which is answered whole, and
 * null for one that asks for none of them.
 */
export function byteRange(header: string, size: number): [number, number] | null | undefined {
  const m = /^bytes=(\d*)-(\d*)$/.exec(header.trim());
  if (m === null || (m[1] === "" && m[2] === "")) {
    return undefined;
  }
  const [first, last] = [m[1] ?? "", m[2] ?? ""];
  const start = first === "" ? Math.max(0, size - Number(last)) : Number(first); // "-n": the last n
  const end = first === "" || last === "" ? size - 1 : Math.min(Number(last), size - 1);
  return start > end || start >= size ? null : [start, end];
}

/**
 * How many bytes of attachments main keeps, the least recently used going first.
 * shortcut: a file larger than this (a long video) is read from kithd again for each
 * part a player asks for; stream it from kithd if long videos are watched in the window.
 */
export const mediaCacheBytes = 64 << 20;

/**
 * Media answers kith-media: URLs with the attachment's bytes, or a picture, kept
 * in memory by the whole URL: its version says what the attachment is, so a picture an
 * edit replaced is a URL of its own, never the old bytes.
 */
export class Media {
  private readonly held = new Map<string, Uint8Array>();
  private size = 0;

  constructor(
    private readonly load: (room: string, event: string) => Promise<Uint8Array>,
    private readonly picture: (room: string, person: string) => Promise<Uint8Array>,
  ) {}

  /**
   * answer is the response to a request for url: the bytes, or why there are none; with
   * range (a Range header), the part it asks for. A player seeks by parts, and it plays a
   * file of more than a few hundred KB only when each answer says its length.
   */
  async answer(url: string, range: string | null = null): Promise<Response> {
    const named = mediaOf(url);
    const pictured = pictureOf(url);
    if (named === undefined && pictured === undefined) {
      return new Response(null, { status: 404 });
    }
    let bytes = this.held.get(url);
    if (bytes !== undefined) {
      this.held.delete(url); // used now: to the back of the line
      this.held.set(url, bytes);
    } else {
      try {
        bytes = await (named
          ? this.load(named.room, named.event)
          : this.picture(pictured?.room ?? "", pictured?.person ?? ""));
      } catch (err) {
        return new Response(err instanceof Error ? err.message : String(err), { status: 502 });
      }
      this.keep(url, bytes);
    }
    const headers = { "Cache-Control": "no-store", "Accept-Ranges": "bytes" };
    const part = range === null ? undefined : byteRange(range, bytes.length);
    if (part === null) {
      return new Response(null, {
        status: 416,
        headers: { ...headers, "Content-Range": `bytes */${String(bytes.length)}` },
      });
    }
    if (part !== undefined) {
      const [start, end] = part;
      const content = `bytes ${String(start)}-${String(end)}/${String(bytes.length)}`;
      return new Response(bytes.slice(start, end + 1), {
        status: 206,
        headers: { ...headers, "Content-Range": content, "Content-Length": String(end - start + 1) },
      });
    }
    return new Response(new Uint8Array(bytes), { headers: { ...headers, "Content-Length": String(bytes.length) } });
  }

  private keep(url: string, bytes: Uint8Array): void {
    if (bytes.length > mediaCacheBytes || this.held.has(url)) {
      return; // too large to keep, or kept meanwhile by a request alike
    }
    this.held.set(url, bytes);
    this.size += bytes.length;
    for (const [oldest, b] of this.held) {
      if (this.size <= mediaCacheBytes) {
        break;
      }
      this.held.delete(oldest);
      this.size -= b.length;
    }
  }
}
