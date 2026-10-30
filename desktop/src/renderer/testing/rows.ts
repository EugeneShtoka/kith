// Messages as the conformance table (internal/domain/testdata/timeline.json) writes
// them, for the window's tests: the fields ordering and merging read, times in Unix
// milliseconds, an absent field its zero.
import { create } from "@bufbuild/protobuf";
import { timestampFromMs, timestampMs } from "@bufbuild/protobuf/wkt";

import { type Message, MessageSchema } from "../../gen/backend/v1/types_pb";

export interface Row {
  id: string;
  ts?: number;
  seq?: number;
  sender?: string;
  senderName?: string;
  body?: string;
  format?: string;
  edited?: boolean;
  editedAt?: number;
  revisionId?: string;
  reverted?: boolean;
  redacted?: boolean;
  redactedBy?: string;
  redactedReason?: string;
  redactedAt?: number;
  replyTo?: string;
  threadRoot?: string;
  mentioned?: boolean;
  placeholder?: boolean;
  undecrypted?: boolean;
  media?: string;
  poll?: string;
}

/** message is r as kithd sends it, in room. */
export function message(r: Row, room = "!r"): Message {
  return create(MessageSchema, {
    id: r.id,
    roomId: room,
    timestamp: r.ts ? timestampFromMs(r.ts) : undefined,
    seq: BigInt(r.seq ?? 0),
    sender: r.sender ?? "",
    senderName: r.senderName ?? "",
    body: r.body ?? "",
    format: r.format ? { text: r.format } : undefined,
    edited: r.edited ?? false,
    editedAtUnixMs: BigInt(r.editedAt ?? 0),
    revisionId: r.revisionId ?? "",
    reverted: r.reverted ?? false,
    redacted: r.redacted ?? false,
    redactedBy: r.redactedBy ?? "",
    redactedReason: r.redactedReason ?? "",
    redactedAtUnixMs: BigInt(r.redactedAt ?? 0),
    replyTo: r.replyTo ?? "",
    threadRoot: r.threadRoot ?? "",
    mentioned: r.mentioned ?? false,
    placeholder: r.placeholder ?? false,
    undecrypted: r.undecrypted ?? false,
    media: r.media ? { type: "image", name: r.media } : undefined,
    poll: r.poll ? { question: r.poll } : undefined,
  });
}

/** row is m as the table writes it. */
export function row(m: Message): Row {
  const r: Row = { id: m.id };
  const set = <K extends keyof Row>(k: K, v: Row[K], none: Row[K]) => {
    if (v !== none) {
      r[k] = v;
    }
  };
  set("ts", m.timestamp ? timestampMs(m.timestamp) : 0, 0);
  set("seq", Number(m.seq), 0);
  set("sender", m.sender, "");
  set("senderName", m.senderName, "");
  set("body", m.body, "");
  set("format", m.format?.text ?? "", "");
  set("edited", m.edited, false);
  set("editedAt", Number(m.editedAtUnixMs), 0);
  set("revisionId", m.revisionId, "");
  set("reverted", m.reverted, false);
  set("redacted", m.redacted, false);
  set("redactedBy", m.redactedBy, "");
  set("redactedReason", m.redactedReason, "");
  set("redactedAt", Number(m.redactedAtUnixMs), 0);
  set("replyTo", m.replyTo, "");
  set("threadRoot", m.threadRoot, "");
  set("mentioned", m.mentioned, false);
  set("placeholder", m.placeholder, false);
  set("undecrypted", m.undecrypted, false);
  set("media", m.media?.name ?? "", "");
  set("poll", m.poll?.question ?? "", "");
  return r;
}
