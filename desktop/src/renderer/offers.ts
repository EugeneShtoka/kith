// The one-time offers the TUI makes when it starts (tui/dictionaries.go,
// completionmodel.go), in its order: the spelling dictionaries this person's own writing
// needs (DetectLanguages, from the cache), then the word counts that catch a typo that
// is also a word, then the local completion model (DetectModel). Each is a list of rows,
// all ticked but word counts kithd does not recommend: what is ticked is installed
// (each checked against its pinned hash in kithd), what is unticked is turned down for
// good in device.toml ([spell] declined, frequencies_declined, [complete.model]
// declined). Dismissed, the offers end until the window starts again; a failure to ask
// is not news, nobody asked for it.
import type { DetectLanguagesResponse, DetectModelResponse, FrequencyCandidate } from "../gen/backend/v1/services_pb";
import type { DeviceSettings } from "../gen/device/v1/device_pb";

/** One row of an offer: what is installed, and what it costs. */
export interface Row {
  tag: string;
  detail: string;
  ticked: boolean;
}

/** An offer on screen. */
export interface Prompt {
  kind: "dictionaries" | "frequencies" | "model";
  title: string;
  rows: readonly Row[];
}

/** What the offers ask of kithd and the relay. */
export interface OfferCalls {
  /** settings reads device.toml's settings, as the relay resolves them. */
  settings(): Promise<DeviceSettings>;
  detectLanguages(): Promise<DetectLanguagesResponse>;
  detectModel(): Promise<DetectModelResponse>;
  install(kind: Prompt["kind"], tag: string): Promise<void>;
  /** decline writes what was turned down, added to what the file holds. */
  decline(kind: Prompt["kind"], tags: readonly string[]): Promise<void>;
}

const titles: Record<Prompt["kind"], string> = {
  dictionaries: "Install spelling dictionaries for what you write?",
  frequencies: "Install word counts, so typos that are also words get caught?",
  model: "Install the completion model, to run on this machine?",
};

const mb = (bytes: bigint) => (Number(bytes) / (1 << 20)).toFixed(1);
const pct = (share: number) => (100 * share).toFixed(0);

/** firstLine is what a status line shows of a long answer. */
function firstLine(s: string): string {
  return s.split("\n", 1)[0] ?? s;
}

function frequencyDetail(c: FrequencyCandidate): string {
  let out = `${c.script} · ${pct(c.share)}% of what you write`;
  if (c.accepts > 0) {
    out += ` · dictionary accepts ${pct(c.accepts)}% of 3-letter strings`;
  }
  return `${out} · ${mb(c.bytes)} MB → ${mb(c.disk)} MB kept`;
}

/** installedNote is what the window says once a kind's downloads ended. */
function installedNote(kind: "dictionaries" | "frequencies", done: string[], failed: string[]): string {
  const d = done.join(", ");
  const f = failed.join(", ");
  if (kind === "frequencies") {
    if (failed.length > 0) {
      return done.length > 0
        ? `word counts installed for ${d}; ${f} could not be verified`
        : `word counts for ${f} could not be verified and were not installed`;
    }
    return `word counts installed for ${d} — rare words are now checked`;
  }
  if (failed.length > 0) {
    return done.length > 0
      ? `installed ${d}; ${f} could not be verified and were not installed`
      : `${f} could not be verified and were not installed`;
  }
  return `installed ${d} — spellcheck is ready`;
}

/**
 * Offers walks the chain once: start asks, accept answers the offer shown (and shows the
 * next), dismiss ends it. What it says (a download under way and how it ended, why there
 * is nothing to check with) is its note.
 */
export class Offers {
  prompt: Prompt | undefined;
  note = "";
  private started = false;
  private frequencies: FrequencyCandidate[] = [];
  private settings: DeviceSettings | undefined;

  constructor(
    private readonly calls: OfferCalls,
    private readonly changed: () => void,
  ) {}

  /** start makes the offers, once: the run has rooms, so the cache has words to count. */
  async start(): Promise<void> {
    if (this.started) {
      return;
    }
    this.started = true;
    let langs: DetectLanguagesResponse;
    try {
      this.settings = await this.calls.settings();
      langs = await this.calls.detectLanguages();
    } catch {
      return;
    }
    if (langs.why !== "") {
      this.say(firstLine(langs.why)); // nothing to check with: say what to install, once
      await this.offerModel();
      return;
    }
    this.frequencies = langs.frequencies;
    const declined = new Set(this.settings.spellDeclined);
    const rows = langs.candidates
      .filter((c) => !declined.has(c.tag))
      .map((c) => ({
        tag: c.tag,
        detail: `${c.script} · ${pct(c.share)}% of what you write · ${mb(c.bytes)} MB`,
        ticked: true,
      }));
    if (rows.length === 0) {
      await this.offerFrequencies();
      return;
    }
    this.show("dictionaries", rows);
  }

  /** accept installs the rows ticked, turns down the rest, and shows the next offer. */
  async accept(ticked: readonly string[]): Promise<void> {
    const shown = this.prompt;
    if (shown === undefined) {
      return;
    }
    this.prompt = undefined;
    this.changed();
    const declined = shown.rows.map((r) => r.tag).filter((tag) => !ticked.includes(tag));
    if (declined.length > 0) {
      await this.calls.decline(shown.kind, declined).catch((err: unknown) => {
        this.say(`could not keep what was turned down: ${err instanceof Error ? err.message : String(err)}`);
      });
    }
    const tags = shown.rows.map((r) => r.tag).filter((tag) => ticked.includes(tag));
    if (shown.kind === "model") {
      if (declined.length === 0) {
        void this.installModel(tags);
      }
      return;
    }
    if (tags.length > 0) {
      void this.installEach(shown.kind, tags);
    }
    if (shown.kind === "dictionaries") {
      await this.offerFrequencies();
    } else {
      await this.offerModel();
    }
  }

  /** dismiss puts the offer away, and the ones after it, until the window starts again. */
  dismiss(): void {
    this.prompt = undefined;
    this.changed();
  }

  /** forget puts the note away. */
  forget(): void {
    this.note = "";
    this.changed();
  }

  private async offerFrequencies(): Promise<void> {
    const declined = new Set(this.settings?.frequenciesDeclined ?? []);
    const rows = this.frequencies
      .filter((c) => !declined.has(c.tag))
      // Only a recommended row is ticked: for some languages the download outweighs how
      // often the check would fire.
      .map((c) => ({ tag: c.tag, detail: frequencyDetail(c), ticked: c.recommended }));
    if (rows.length === 0) {
      await this.offerModel();
      return;
    }
    this.show("frequencies", rows);
  }

  private async offerModel(): Promise<void> {
    if (this.settings === undefined || !this.settings.complete || this.settings.modelDeclined) {
      return;
    }
    let found: DetectModelResponse;
    try {
      found = await this.calls.detectModel();
    } catch {
      return;
    }
    if (found.why !== "") {
      this.say(firstLine(found.why)); // llama.cpp is missing: say what to install, once
      return;
    }
    const c = found.candidate;
    if (!found.offer || c === undefined) {
      return;
    }
    this.show("model", [
      {
        tag: c.tag,
        detail:
          `${c.name} · finishes the word you were about to type ${pct(c.recall)}% of the time, in ~20ms · ` +
          `${(Number(c.bytes) / (1 << 20)).toFixed(0)} MB · runs here, over no network`,
        ticked: true,
      },
    ]);
  }

  private show(kind: Prompt["kind"], rows: Row[]): void {
    this.prompt = { kind, title: titles[kind], rows };
    this.changed();
  }

  private say(note: string): void {
    this.note = note;
    this.changed();
  }

  private async installEach(kind: "dictionaries" | "frequencies", tags: string[]): Promise<void> {
    this.say(`fetching ${kind === "frequencies" ? "word counts for " : ""}${tags.join(", ")}…`);
    const done: string[] = [];
    const failed: string[] = [];
    for (const tag of tags) {
      try {
        await this.calls.install(kind, tag);
        done.push(tag);
      } catch {
        failed.push(tag); // a checksum that does not match is a refusal, not a retry
      }
    }
    this.say(installedNote(kind, done, failed));
  }

  private async installModel(tags: string[]): Promise<void> {
    const [tag] = tags;
    if (tag === undefined) {
      return;
    }
    this.say("fetching the completion model — it will start answering when it lands…");
    try {
      await this.calls.install("model", tag);
      this.say("completion model installed — your words are finished on this machine now");
    } catch (err) {
      this.say(
        `the completion model was not installed: ${firstLine(err instanceof Error ? err.message : String(err))}`,
      );
    }
  }
}
