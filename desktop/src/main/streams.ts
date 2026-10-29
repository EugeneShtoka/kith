import { type DescMessage, type DescMethodServerStreaming, type MessageShape, toBinary } from "@bufbuild/protobuf";
import type { Transport } from "@connectrpc/connect";

import type { StreamEvent } from "../shared/ipc";

/**
 * How long a stream waits before opening again: first, doubling up to most. A stream
 * that stayed open most or longer starts from first again; one ended quickly keeps
 * climbing, so a kithd that ends every stream at once is not asked again every 250 ms.
 */
export const backoff = { first: 250, most: 5000 };

/** How long frames gather before they are posted together, in milliseconds. */
export const batchFor = 16;

/**
 * keepOpen holds a server stream open for the window until the returned stop is called:
 * opened again after any end (kithd restarting, the relay failing) after a wait, with
 * frames batched so a burst is one message. Every frame of an attempt is posted before
 * the end that follows it. Every attempt after the first sends again, when given, in
 * place of input.
 */
export function keepOpen<I extends DescMessage, O extends DescMessage>(
  transport: Transport,
  method: DescMethodServerStreaming<I, O>,
  input: MessageShape<I>,
  header: HeadersInit,
  post: (event: StreamEvent) => void,
  again: MessageShape<I> = input,
): () => void {
  const stopped = new AbortController();
  let pending: Uint8Array[] = [];
  let timer: ReturnType<typeof setTimeout> | undefined;
  const flush = () => {
    clearTimeout(timer);
    timer = undefined;
    if (pending.length > 0) {
      post({ kind: "frames", frames: pending });
      pending = [];
    }
  };

  let request = input;
  const attempt = async (): Promise<void> => {
    const response = await transport.stream(method, stopped.signal, undefined, header, once(request));
    request = again;
    post({ kind: "attached" });
    for await (const message of response.message) {
      pending.push(toBinary(method.output, message));
      timer ??= setTimeout(flush, batchFor);
    }
  };

  void (async () => {
    let wait = backoff.first;
    for (;;) {
      const began = Date.now();
      let error = "the stream ended";
      try {
        await attempt();
      } catch (err) {
        error = err instanceof Error ? err.message : String(err);
      }
      if (stopped.signal.aborted) {
        return; // nobody is listening: the window closed the stream
      }
      flush();
      post({ kind: "detached", error });
      if (Date.now() - began >= backoff.most) {
        wait = backoff.first;
      }
      await sleep(wait, stopped.signal);
      wait = Math.min(wait * 2, backoff.most);
    }
  })();

  return () => {
    stopped.abort();
    clearTimeout(timer);
  };
}

async function* once<T>(value: T): AsyncIterable<T> {
  yield await Promise.resolve(value);
}

function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const timer = setTimeout(done, ms);
    signal.addEventListener("abort", done, { once: true });
    function done() {
      clearTimeout(timer);
      signal.removeEventListener("abort", done);
      resolve();
    }
  });
}
