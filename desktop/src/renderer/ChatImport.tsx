// The settings page's chat import: a chat a phone exported, brought into a chat's
// history, as the TUI's :import → A WhatsApp chat export (chatimport.ts leads it).
import { useState } from "react";

import type { ChatImportSendersResponse, PreviewChatImportResponse } from "../gen/backend/v1/services_pb";
import type { Member, Room } from "../gen/backend/v1/types_pb";
import { type ImportView, plural } from "./chatimport";
import { nameOf } from "./mentions";
import type { Session } from "./session";

const day = new Intl.DateTimeFormat(undefined, { dateStyle: "medium" });

export function ChatImportSection({
  chatImport,
  rooms,
  session,
}: {
  chatImport: ImportView;
  rooms: readonly Room[];
  session: Session;
}) {
  const { step, note, preview, senders } = chatImport;
  return (
    <>
      <h3>Import a chat</h3>
      <p className="detail">
        A chat's older history, from the phone's Export chat (WhatsApp's: the .zip, or the .txt in it). Only what is
        older than the chat's oldest message here is added, as text.
      </p>
      {note && <p className="banner">{note}</p>}
      {step === "idle" && (
        <p className="login-actions">
          <button
            type="button"
            onClick={() => {
              session.chooseExport();
            }}
          >
            Choose the export
          </button>
        </p>
      )}
      {step === "chat" && preview && <Chats preview={preview} rooms={rooms} session={session} />}
      {step === "senders" && senders && <Senders chatImport={chatImport} senders={senders} session={session} />}
    </>
  );
}

function nameIn(rooms: readonly Room[], id: string): string {
  const room = rooms.find((r) => r.id === id);
  return room?.name || id;
}

/** Chats offers the chats kithd says the export may be, and any other. */
function Chats({
  preview,
  rooms,
  session,
}: {
  preview: PreviewChatImportResponse;
  rooms: readonly Room[];
  session: Session;
}) {
  const [other, setOther] = useState("");
  const span = `${day.format(new Date(Number(preview.firstMs)))} – ${day.format(new Date(Number(preview.lastMs)))}`;
  const sorted = [...rooms].sort((a, b) => (a.name || a.id).localeCompare(b.name || b.id));
  return (
    <section className="login" aria-label="Import into which chat">
      <h4>
        <span dir="auto">{preview.title}</span>: {plural(preview.messages, "message")}, {span}
      </h4>
      <ul className="networks">
        {preview.rooms.map((r) => {
          const why = [
            r.found > 0 ? `has ${plural(r.found, "message")} of it` : "",
            r.named ? "the file is named after it" : "",
            `adds ${plural(r.adds, "older message")}`,
          ].filter((w) => w !== "");
          return (
            <li key={r.roomId} className="account">
              <span dir="auto">{nameIn(rooms, r.roomId)}</span>
              <span className="detail">{why.join(" · ")}</span>
              <button
                type="button"
                onClick={() => {
                  session.importInto(r.roomId, nameIn(rooms, r.roomId));
                }}
              >
                Import here
              </button>
            </li>
          );
        })}
        <li className="account">
          <label>
            Somewhere else{" "}
            <select
              value={other}
              onChange={(e) => {
                setOther(e.target.value);
              }}
            >
              <option value="">a chat</option>
              {sorted.map((r) => (
                <option key={r.id} value={r.id}>
                  {r.name || r.id}
                </option>
              ))}
            </select>
          </label>
          <button
            type="button"
            disabled={other === ""}
            onClick={() => {
              session.importInto(other, nameIn(rooms, other));
            }}
          >
            Import there
          </button>
        </li>
      </ul>
      <p className="login-actions">
        <button
          type="button"
          onClick={() => {
            session.cancelImport();
          }}
        >
          Cancel
        </button>
      </p>
    </section>
  );
}

/** Senders lists the export's names, each taken as a member or as written. */
function Senders({
  chatImport,
  senders,
  session,
}: {
  chatImport: ImportView;
  senders: ChatImportSendersResponse;
  session: Session;
}) {
  return (
    <section className="login" aria-label="Who wrote it">
      <h4>
        Who wrote it, in <span dir="auto">{chatImport.roomName}</span>
      </h4>
      <ul className="networks">
        {senders.senders.map((s) => {
          const likely = new Set(s.likely.map((m) => m.userId));
          const others = senders.members.filter((m) => !likely.has(m.userId));
          return (
            <li key={s.name} className="account">
              <label>
                <span dir="auto">{s.name}</span> <span className="detail">{plural(s.messages, "message")}</span>{" "}
                <select
                  value={chatImport.who[s.name] ?? ""}
                  onChange={(e) => {
                    session.importAs(s.name, e.target.value);
                  }}
                >
                  {s.likely.map((m) => (
                    <MemberOption key={m.userId} member={m} />
                  ))}
                  {others.map((m) => (
                    <MemberOption key={m.userId} member={m} />
                  ))}
                  <option value="">as written, no one's</option>
                </select>
              </label>
            </li>
          );
        })}
      </ul>
      <p className="login-actions">
        <button
          type="button"
          onClick={() => {
            session.applyImport();
          }}
        >
          Import {plural(senders.adds, "older message")}
        </button>
        <button
          type="button"
          onClick={() => {
            session.importBack();
          }}
        >
          Back
        </button>
        <button
          type="button"
          onClick={() => {
            session.cancelImport();
          }}
        >
          Cancel
        </button>
      </p>
    </section>
  );
}

function MemberOption({ member }: { member: Member }) {
  return <option value={member.userId}>{nameOf(member)}</option>;
}
