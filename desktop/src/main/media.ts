import { mediaOf } from "../shared/media";

/** How many bytes of pictures main keeps, the least recently used going first. */
export const mediaCacheBytes = 64 << 20;

/**
 * Media answers kith-media: URLs with the attachment's bytes, kept in memory by the
 * whole URL: its version says what the attachment is, so a picture an edit replaced is
 * a URL of its own, never the old bytes.
 */
export class Media {
  private readonly held = new Map<string, Uint8Array>();
  private size = 0;

  constructor(private readonly load: (room: string, event: string) => Promise<Uint8Array>) {}

  /** answer is the response to a request for url: the bytes, or why there are none. */
  async answer(url: string): Promise<Response> {
    const named = mediaOf(url);
    if (named === undefined) {
      return new Response(null, { status: 404 });
    }
    let bytes = this.held.get(url);
    if (bytes !== undefined) {
      this.held.delete(url); // used now: to the back of the line
      this.held.set(url, bytes);
    } else {
      try {
        bytes = await this.load(named.room, named.event);
      } catch (err) {
        return new Response(err instanceof Error ? err.message : String(err), { status: 502 });
      }
      this.keep(url, bytes);
    }
    return new Response(new Uint8Array(bytes), { headers: { "Cache-Control": "no-store" } });
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
