// A chat a phone exported (WhatsApp's Export chat), brought into a chat's history as the
// TUI's :import does: main picks the file, brings it to kithd and has it read; the window
// chooses the chat among kithd's offers or any other, then who each name is, then
// imports what is older than the chat's oldest message.
import { fromBinary } from "@bufbuild/protobuf";

import {
  type ChatImportSendersResponse,
  type PreviewChatImportResponse,
  PreviewChatImportResponseSchema,
} from "../gen/backend/v1/services_pb";
import type { ChatChosen } from "../shared/ipc";

/** An import's calls: main's (the dialog, the upload, kithd's preview), then kithd's. */
export interface ImportCalls {
  choose(): Promise<ChatChosen | undefined>;
  senders(upload: string, zone: string, room: string): Promise<ChatImportSendersResponse>;
  apply(upload: string, zone: string, room: string, senders: Readonly<Record<string, string>>): Promise<number>;
}

/** An import as the settings page shows it. */
export interface ImportView {
  /** Nothing under way; waiting on kithd (note says for what); choosing the chat; who wrote it. */
  step: "idle" | "busy" | "chat" | "senders";
  note: string;
  preview: PreviewChatImportResponse | undefined;
  /** The chat chosen, and what the settings page calls it. */
  room: string;
  roomName: string;
  senders: ChatImportSendersResponse | undefined;
  /** Who each name is taken as: a member's ID, "" the name as written. */
  who: Readonly<Record<string, string>>;
}

export const idleImport: ImportView = {
  step: "idle",
  note: "",
  preview: undefined,
  room: "",
  roomName: "",
  senders: undefined,
  who: {},
};

/**
 * ChatImport leads one import at a time. An answer for an import canceled or moved on
 * from meanwhile is dropped; a step that fails goes back to the one before, said why.
 */
export class ChatImport {
  private view = idleImport;
  /** Counts the steps asked: an answer for an earlier one is dropped. */
  private gen = 0;
  private chosen: ChatChosen | undefined;

  constructor(
    private readonly calls: ImportCalls,
    private readonly changed: (view: ImportView) => void,
  ) {}

  snapshot(): ImportView {
    return this.view;
  }

  /** choose asks main for the export, which kithd reads; none picked changes nothing. */
  choose(): void {
    if (this.view.step === "busy") {
      return;
    }
    const gen = ++this.gen;
    this.set({ ...idleImport, step: "busy", note: "Reading the export…" });
    this.calls.choose().then(
      (chosen) => {
        if (gen !== this.gen) {
          return;
        }
        this.chosen = chosen;
        this.set(
          chosen === undefined
            ? idleImport
            : { step: "chat", note: "", preview: fromBinary(PreviewChatImportResponseSchema, chosen.preview) },
        );
      },
      (err: unknown) => {
        this.failed(gen, { ...idleImport, note: `Could not read the export: ${errorText(err)}` });
      },
    );
  }

  /** room matches the export's names to room's members; name is what the page calls it. */
  room(room: string, name: string): void {
    const chosen = this.chosen;
    if (chosen === undefined || this.view.step !== "chat") {
      return;
    }
    const gen = ++this.gen;
    this.set({ step: "busy", note: "Matching who wrote it…", room, roomName: name });
    this.calls.senders(chosen.upload, chosen.zone, room).then(
      (senders) => {
        if (gen !== this.gen) {
          return;
        }
        const who: Record<string, string> = {};
        for (const s of senders.senders) {
          if (s.chosen !== "") {
            who[s.name] = s.chosen;
          }
        }
        this.set({ step: "senders", note: "", senders, who });
      },
      (err: unknown) => {
        this.failed(gen, { step: "chat", note: `Could not match who wrote it: ${errorText(err)}` });
      },
    );
  }

  /** whoIs takes name as the member id ("" as written). */
  whoIs(name: string, id: string): void {
    if (this.view.step === "senders") {
      this.set({ who: { ...this.view.who, [name]: id } });
    }
  }

  /** back goes from who wrote it back to choosing the chat. */
  back(): void {
    if (this.view.step === "senders") {
      this.gen++;
      this.set({ step: "chat", note: "", senders: undefined, who: {} });
    }
  }

  /** apply imports the export into the chat chosen, each name as chosen. */
  apply(): void {
    const chosen = this.chosen;
    if (chosen === undefined || this.view.step !== "senders") {
      return;
    }
    const gen = ++this.gen;
    const { room, roomName, who } = this.view;
    this.set({ step: "busy", note: "Importing the chat…" });
    this.calls.apply(chosen.upload, chosen.zone, room, who).then(
      (added) => {
        if (gen !== this.gen) {
          return;
        }
        this.chosen = undefined; // kithd used the upload up
        this.set({ ...idleImport, note: `Imported ${plural(added, "message")} into ${roomName}.` });
      },
      (err: unknown) => {
        this.failed(gen, { step: "senders", note: `Could not import the chat: ${errorText(err)}` });
      },
    );
  }

  /** cancel drops the import; what kithd staged it removes once idle for an hour. */
  cancel(): void {
    this.gen++;
    this.chosen = undefined;
    this.set(idleImport);
  }

  private failed(gen: number, change: Partial<ImportView>): void {
    if (gen === this.gen) {
      this.set(change);
    }
  }

  private set(change: Partial<ImportView>): void {
    this.view = { ...this.view, ...change };
    this.changed(this.view);
  }
}

/** plural is n with word, plural when not one. */
export function plural(n: number, word: string): string {
  return `${n.toLocaleString()} ${word}${n === 1 ? "" : "s"}`;
}

function errorText(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}
