import { hostname } from "node:os";

import {
  create,
  type DescMessage,
  fromBinary,
  type MessageInitShape,
  type MessageShape,
  toBinary,
} from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { BackendService, type LoginStep, LoginStepSchema, type SeatRequest } from "../gen/backend/v1/services_pb";
import { MessageSchema, VerificationKind } from "../gen/backend/v1/types_pb";
import { DeviceService } from "../gen/device/v1/device_pb";
import type { StreamEvent } from "../shared/ipc";
import { type Frame, Gate, Refused } from "./gate";
import { FakeKithd, until } from "./testing/kithd";

const window: Frame = { url: "app://kith/index.html", parent: null };
const backend = BackendService.method;

/** bytes is a request as the window sends it. */
function bytes<I extends DescMessage>(schema: I, init: MessageInitShape<I> = {} as MessageInitShape<I>): Uint8Array {
  return toBinary(schema, create(schema, init));
}

const empty = new Uint8Array();

let kithd: FakeKithd;
let gate: Gate;

beforeEach(async () => {
  kithd = new FakeKithd();
  kithd.profiles = ["Home", "Work"];
  kithd.remembered = "Work";
  kithd.invited = ["!in:x"];
  await kithd.start();
  gate = new Gate(kithd.transport());
});

afterEach(async () => {
  await kithd.dispose();
});

/** outcome is what the gate did with a call: sent to kithd (answered or not), or refused before it. */
async function outcome(frame: Frame | null, name: unknown, request: unknown): Promise<"answered" | "refused"> {
  try {
    await gate.call(frame, name, request);
    return "answered";
  } catch (err) {
    if (err instanceof Refused) {
      return "refused";
    }
    return "answered"; // kithd's own error: the call reached it
  }
}

/** opened is what the gate did with a stream: kept it open, or refused it. */
function opened(frame: Frame | null, name: unknown, request: unknown): "opened" | "refused" {
  try {
    gate.subscribe(frame, name, request, () => undefined)();
    return "opened";
  } catch (err) {
    if (err instanceof Refused) {
      return "refused";
    }
    throw err;
  }
}

/** listen opens the stream named, keeping what it posts. */
function listen(name: string, request: Uint8Array = empty) {
  const events: StreamEvent[] = [];
  const stop = gate.subscribe(window, name, request, (e) => events.push(e));
  return { events, stop };
}

/** device reads the device through the gate, as the window does first. */
async function device(): Promise<void> {
  await gate.call(window, DeviceService.method.device.name, empty);
}

// The window's whole scope: a call or stream joins the list only with the PR that needs it.
const allowedCalls = [
  "Status",
  "Rail",
  "Rooms",
  "LastMessages",
  "CachedUnread",
  "CachedInvites",
  "JoinRoom",
  "LeaveRoom",
  "CachedTimeline",
  "Timeline",
  "MessagesAround",
  "FetchEvent",
  "CachedReactions",
  "MarkRead",
  "Send",
  "Redact",
  "SendReaction",
  "SendTyping",
  "MentionCandidates",
  "EmojiScores",
  "RecordEmoji",
  "Selves",
  "Drafts",
  "ReplaceDraft",
  "VotePoll",
  "PollVoters",
  "CreatePoll",
  "SearchMessages",
  "ThreadPage",
  "MarkThreadRead",
  "Present",
  "Device",
  "ChooseProfile",
  "DeviceSettings",
  "UpdateDeviceSettings",
  "Theme",
  "UpdateTheme",
  "SenderSlots",
  "LoginNetworks",
  "BeginLogin",
  "AnswerLogin",
  "CancelLogin",
  "InsteadLogin",
  "WriteAccount",
  "QRCode",
  "SignOut",
  "Accounts",
  "StartVerification",
  "AcceptVerification",
  "ConfirmSAS",
  "CancelVerification",
  "CheckSpelling",
  "LearnWord",
  "AllowRareWord",
  "CompleteWord",
  "ModelTask",
  "DetectLanguages",
  "DetectModel",
  "InstallDictionary",
  "InstallFrequencies",
  "InstallModel",
  "Schedule",
  "ScheduledMessages",
  "CancelScheduled",
  "RetryScheduled",
  "Commands",
  "RunCommand",
  "FilingPlaces",
  "FileRoom",
];
const allowedStreams = [
  "Changed",
  "ConfigChanged",
  "Seat",
  "UnreadStream",
  "Invites",
  "Messages",
  "Reactions",
  "FollowStream",
  "Verifications",
];

// A request each listed call is answered for: an invitation's room, a profile the config has.
const requests: Record<string, Uint8Array> = {
  JoinRoom: bytes(backend.joinRoom.input, { roomIdOrAlias: "!in:x" }),
  LeaveRoom: bytes(backend.leaveRoom.input, { roomId: "!in:x" }),
  ChooseProfile: bytes(DeviceService.method.chooseProfile.input, { name: "Work" }),
  // Of the logins begun first (leading), each at a step that allows the call.
  AnswerLogin: bytes(backend.answerLogin.input, { login: "l1" }),
  CancelLogin: bytes(backend.cancelLogin.input, { login: "l2" }),
  InsteadLogin: bytes(backend.insteadLogin.input, { login: "l3" }),
  WriteAccount: bytes(DeviceService.method.writeAccount.input, { table: "whatsapp.account", values: { name: "l4" } }),
  SignOut: bytes(backend.signOut.input, { spaceId: "telegram:1/account" }),
  // Of the verification asked for first (asking).
  AcceptVerification: bytes(backend.acceptVerification.input, { txnId: "v1" }),
  ConfirmSAS: bytes(backend.confirmSAS.input, { txnId: "v1" }),
  CancelVerification: bytes(backend.cancelVerification.input, { txnId: "v1" }),
  // The next word alone, and only what kithd offered (asked first: offered).
  ModelTask: bytes(backend.modelTask.input, { task: "complete", roomId: "!r:x", draft: "one two three " }),
  InstallDictionary: bytes(backend.installDictionary.input, { tag: "he_IL" }),
  InstallFrequencies: bytes(backend.installFrequencies.input, { tag: "he_IL" }),
  InstallModel: bytes(backend.installModel.input, { tag: "model-1" }),
  // A message the queue showed the window (asked first: ScheduledMessages).
  CancelScheduled: bytes(backend.cancelScheduled.input, { id: "q1" }),
  RetryScheduled: bytes(backend.retryScheduled.input, { id: "q1" }),
};

/** offered asks kithd what may be installed, through the gate. */
async function offered(): Promise<void> {
  await gate.call(window, "DetectLanguages", empty);
  await gate.call(window, "DetectModel", empty);
}

/** step is a login's step: a QR code offering another way, or a new account to write. */
function qrStep(login: string): LoginStep {
  return create(LoginStepSchema, { login, qr: "2@x", instead: "with a code instead" });
}
function configureStep(login: string): LoginStep {
  return create(LoginStepSchema, { login, configure: { table: "whatsapp.account", values: { name: login } } });
}

/** leading begins logins l1 to l4 through the gate: the window leads them. */
async function leading(): Promise<void> {
  kithd.beginWith = [qrStep, qrStep, qrStep, configureStep];
  for (let i = 0; i < 4; i++) {
    await gate.call(window, "BeginLogin", bytes(backend.beginLogin.input, { network: "whatsapp" }));
  }
}

// Every name the window could send: each method of both services by its name and in
// other spellings, and names an object map would answer for itself.
const names: unknown[] = [
  ...[...BackendService.methods, ...DeviceService.methods].flatMap((m) => [
    m.name,
    m.localName,
    `${m.parent.typeName}/${m.name}`,
    `/${m.parent.typeName}/${m.name}`,
    ` ${m.name}`,
  ]),
  "constructor",
  "__proto__",
  "toString",
  "hasOwnProperty",
  "",
  42,
  null,
  undefined,
  { toString: () => "Status" },
  ["Status"],
];

describe("the gate", () => {
  it("answers only the calls on the list, and refuses every other name before kithd", async () => {
    await device();
    await leading();
    await gate.call(window, "StartVerification", bytes(backend.startVerification.input, { on: "matrix:1/" }));
    await offered();
    kithd.queue.push("q1");
    await gate.call(window, "ScheduledMessages", empty);
    kithd.accountSpaces = ["telegram:1/account"];
    const seat = listen("Seat", bytes(backend.seat.input, { client: "w" })); // drafts are the seat's
    for (const name of names) {
      const want = typeof name === "string" && allowedCalls.includes(name) ? "answered" : "refused";
      const request = typeof name === "string" ? (requests[name] ?? empty) : empty;
      expect(await outcome(window, name, request), String(name)).toBe(want);
    }
    const reached = new Set(kithd.reached.map((r) => r.split(":")[0]));
    reached.delete("Seat");
    seat.stop();
    expect([...reached].sort()).toEqual([...allowedCalls].sort());
  });

  it("installs only what kithd offered, and asks the model only for the next word", async () => {
    await device();
    const install = (name: string, tag: string) =>
      outcome(window, name, bytes(backend.installDictionary.input, { tag }));
    const model = (task: string, dryRun = false) =>
      outcome(window, "ModelTask", bytes(backend.modelTask.input, { task, roomId: "!r:x", dryRun }));

    // Before an offer, nothing; after it, what it named, each by the call that installs its kind.
    expect(await install("InstallDictionary", "he_IL")).toBe("refused");
    expect(await install("InstallModel", "model-1")).toBe("refused");
    await offered();
    expect(await install("InstallDictionary", "he_IL")).toBe("answered");
    expect(await install("InstallFrequencies", "he_IL")).toBe("answered");
    expect(await install("InstallModel", "model-1")).toBe("answered");
    expect(await install("InstallDictionary", "de_DE")).toBe("refused");
    expect(await install("InstallDictionary", "model-1")).toBe("refused");
    expect(await install("InstallModel", "he_IL")).toBe("refused");

    // A task that sends a conversation to [assist]'s endpoint, or a dry run showing one, never.
    expect(await model("complete")).toBe("answered");
    for (const task of ["summary", "todo", "rewrite", ""]) {
      expect(await model(task), task).toBe("refused");
    }
    expect(await model("complete", true)).toBe("refused");
    expect(kithd.reached.filter((r) => r.startsWith("ModelTask"))).toHaveLength(1);
  });

  it("writes a draft as the window's seat in its profile, whatever the window names, and none without one", async () => {
    await device();
    const draft = (client: string) =>
      outcome(
        window,
        "ReplaceDraft",
        bytes(backend.replaceDraft.input, { client, draft: { roomId: "!r:x", body: "hi" } }),
      );
    const written = () =>
      kithd.received
        .filter((r) => r.method === "ReplaceDraft")
        .map((r) => (r.message as MessageShape<typeof backend.replaceDraft.input>).client);

    expect(await draft("")).toBe("refused");
    expect(await draft("someone-else")).toBe("refused");
    const seat = listen("Seat", bytes(backend.seat.input, { client: "w" }));
    expect(await draft("")).toBe("answered");
    expect(await draft("someone-else")).toBe("answered");
    expect(written()).toEqual(["w", "w"]);
    // Stepped aside (the stream closed), it writes no more.
    seat.stop();
    expect(await draft("w")).toBe("refused");
    // A seat in another profile is not this one's.
    const other = listen("Seat", bytes(backend.seat.input, { client: "w" }));
    await gate.call(window, "ChooseProfile", bytes(DeviceService.method.chooseProfile.input, { name: "Home" }));
    expect(await draft("w")).toBe("refused");
    other.stop();
    expect(written()).toEqual(["w", "w"]);
  });

  it("listens for links and tells what is open as the window's seat, whatever the window names, and not without one", async () => {
    await device();
    const as = (client: string) => bytes(backend.followStream.input, { client });
    const present = (client: string) =>
      outcome(window, "Present", bytes(backend.present.input, { client, openRoomId: "!r:x" }));
    expect(await present("someone-else")).toBe("refused");
    expect(opened(window, "FollowStream", as("someone-else"))).toBe("refused");
    const seat = listen("Seat", bytes(backend.seat.input, { client: "w" }));
    expect(await present("someone-else")).toBe("answered");
    const links = listen("FollowStream", as("someone-else"));
    await until(() => kithd.received.some((r) => r.method === "FollowStream"), "the link stream to reach kithd");
    const listening = kithd.received.filter((r) => r.method === "FollowStream");
    expect(listening.map((r) => (r.message as MessageShape<typeof backend.followStream.input>).client)).toEqual(["w"]);
    links.stop();
    seat.stop();
  });

  it("opens only the streams on the list, and refuses every other name before kithd", async () => {
    await device();
    const seat = listen("Seat", bytes(backend.seat.input, { client: "w" })); // links are the seat's
    for (const name of names) {
      const want = typeof name === "string" && allowedStreams.includes(name) ? "opened" : "refused";
      expect(opened(window, name, empty), String(name)).toBe(want);
    }
    seat.stop();
  });

  // Electron's WebFrameMain: the window's top frame, or anything that is not it.
  const frames: [Frame | null, boolean][] = [
    [window, true],
    [{ url: "app://kith/", parent: null }, true],
    [{ url: "app://kith/index.html", parent: {} }, false],
    [null, false],
    [{ url: "app://evil/index.html", parent: null }, false],
    [{ url: "app://kith.evil/index.html", parent: null }, false],
    [{ url: "app://kith@evil/index.html", parent: null }, false],
    [{ url: "app://kith:1/index.html", parent: null }, false],
    [{ url: "http://kith/index.html", parent: null }, false],
    [{ url: "https://kith/index.html", parent: null }, false],
    [{ url: "file:///index.html", parent: null }, false],
    [{ url: "about:blank", parent: null }, false],
    [{ url: "devtools://devtools/bundled/index.html", parent: null }, false],
    [{ url: "not a url", parent: null }, false],
    [{ url: "", parent: null }, false],
  ];

  it("answers the window's top frame alone", async () => {
    await device();
    for (const [frame, allowed] of frames) {
      const what = JSON.stringify(frame);
      expect(await outcome(frame, "Status", empty), what).toBe(allowed ? "answered" : "refused");
      expect(await outcome(frame, "Device", empty), what).toBe(allowed ? "answered" : "refused");
      expect(opened(frame, "Changed", empty), what).toBe(allowed ? "opened" : "refused");
    }
    expect(kithd.received.filter((r) => r.method === "Status")).toHaveLength(frames.filter(([, ok]) => ok).length);
  });

  it("refuses a request that is not its method's, and what reaches kithd is only the method's own fields", async () => {
    await device();
    // Not bytes at all, then bytes: random (most malformed, some well-formed wire data of
    // fields StatusRequest does not have), and the cases that matter named.
    const requests: unknown[] = ["", "\u0008\u0001", [8, 1], new Uint8Array([8, 1]).buffer, null, undefined, {}, 7];
    let seed = 1;
    const random = () => (seed = (seed * 1103515245 + 12345) % 2 ** 31) / 2 ** 31;
    for (let i = 0; i < 300; i++) {
      requests.push(Uint8Array.from({ length: Math.floor(random() * 12) }, () => Math.floor(random() * 256)));
    }
    requests.push(new Uint8Array(), new Uint8Array([8, 1]), new Uint8Array([0xff]), new Uint8Array([0x0a, 0x05, 1]));

    for (const request of requests) {
      const got = await outcome(window, "Status", request);
      if (!(request instanceof Uint8Array)) {
        expect(got, String(request)).toBe("refused");
      }
    }
    expect(kithd.received.length).toBeGreaterThan(0);
    for (const r of kithd.received) {
      expect(r.message.$unknown, "fields StatusRequest does not have reached kithd").toBeUndefined();
    }
  });

  it("sends nothing to kithd before a profile is chosen, and the one chosen with every call and stream after", async () => {
    // First connect with several profiles, none remembered: kithd would answer a request
    // naming none for the config's first.
    kithd.remembered = "";
    const tryAll = async () => [
      await outcome(window, "Status", empty),
      await outcome(window, "Rooms", empty),
      opened(window, "Changed", empty),
      opened(window, "Seat", empty),
    ];
    expect(await tryAll()).toEqual(["refused", "refused", "refused", "refused"]);
    await device();
    expect(await tryAll()).toEqual(["refused", "refused", "refused", "refused"]);
    expect(
      await outcome(window, "ChooseProfile", bytes(DeviceService.method.chooseProfile.input, { name: "nope" })),
    ).toBe("answered");
    expect(await tryAll()).toEqual(["refused", "refused", "refused", "refused"]);
    expect(kithd.reached).toEqual(["Device:", "ChooseProfile:"]);

    await gate.call(window, "ChooseProfile", bytes(DeviceService.method.chooseProfile.input, { name: "work" }));
    const s = listen("Changed");
    await until(() => s.events.some((e) => e.kind === "attached"), "the stream to attach");
    await gate.call(window, "Status", empty);
    s.stop();
    expect(kithd.reached.slice(3)).toEqual(["Changed:Work", "Status:Work"]);

    // One profile, none remembered: it is run unasked.
    gate.restart();
    expect(await outcome(window, "Status", empty)).toBe("refused");
    kithd.profiles = ["Solo"];
    await device();
    await gate.call(window, "Status", empty);
    expect(kithd.reached.at(-1)).toBe("Status:Solo");
  });

  it("ends every stream of a profile the window leaves, and runs in none when a reload removes its profile", async () => {
    await device();
    const changed = listen("Changed");
    const unread = listen("UnreadStream");
    await until(() => kithd.streams(backend.changed) === 1 && kithd.streams(backend.unreadStream) === 1, "both open");

    // The device file changed under the window (another writer): it stays where it runs.
    kithd.remembered = "Home";
    await device();
    await gate.call(window, "Status", empty);
    expect(kithd.reached.at(-1)).toBe("Status:Work");
    expect(changed.events.some((e) => e.kind === "refused")).toBe(false);

    // A reload removes the profile, and there are several left: the window asks.
    kithd.profiles = ["Home", "Play"];
    kithd.remembered = "";
    await device();
    for (const s of [changed, unread]) {
      expect(s.events.at(-1)?.kind).toBe("refused");
    }
    await until(
      () => kithd.streams(backend.changed) + kithd.streams(backend.unreadStream) === 0,
      "both ended at kithd",
    );
    expect(await outcome(window, "Status", empty)).toBe("refused");

    // Switching profile ends the old one's streams too.
    await gate.call(window, "ChooseProfile", bytes(DeviceService.method.chooseProfile.input, { name: "Home" }));
    const home = listen("Changed");
    await until(() => kithd.streams(backend.changed) === 1, "Home's stream open");
    await gate.call(window, "ChooseProfile", bytes(DeviceService.method.chooseProfile.input, { name: "Play" }));
    expect(home.events.at(-1)?.kind).toBe("refused");
    await until(() => kithd.streams(backend.changed) === 0, "Home's stream ended at kithd");
    changed.stop();
    unread.stop();
    home.stop();
  });

  it("asks for the seat from this machine, and by force only once", async () => {
    await device();
    const seat = listen(
      "Seat",
      bytes(backend.seat.input, { client: "w", force: true, where: { pid: 7n, host: "elsewhere", sinceUnixMs: 5n } }),
    );
    const seats = () => kithd.received.filter((r) => r.method === "Seat").map((r) => r.message as SeatRequest);
    await until(() => seats().length === 1, "the seat asked for");
    await kithd.stop();
    await kithd.start();
    await until(() => seats().length === 2, "the seat asked for again on the new kithd");
    seat.stop();

    expect(seats().map((s) => s.force)).toEqual([true, false]);
    for (const s of seats()) {
      expect(s.client).toBe("w");
      expect(s.where?.pid).toBe(BigInt(process.pid));
      expect(s.where?.host).toBe(hostname());
      expect(s.where?.sinceUnixMs).toBe(5n);
    }
  });

  it("joins or leaves only a room the profile is invited to", async () => {
    await device();
    const leave = (roomId: string) => outcome(window, "LeaveRoom", bytes(backend.leaveRoom.input, { roomId }));
    const join = (roomIdOrAlias: string) =>
      outcome(window, "JoinRoom", bytes(backend.joinRoom.input, { roomIdOrAlias }));
    expect(await leave("!joined:x")).toBe("refused");
    expect(await leave("")).toBe("refused");
    expect(await join("#somewhere:x")).toBe("refused");
    expect(await join("!other:x")).toBe("refused");
    expect(kithd.reached.filter((r) => r.startsWith("LeaveRoom") || r.startsWith("JoinRoom"))).toEqual([]);
    expect(await leave("!in:x")).toBe("answered");
    expect(await join("!in:x")).toBe("answered");
    expect(kithd.reached.filter((r) => r.startsWith("LeaveRoom") || r.startsWith("JoinRoom"))).toEqual([
      "LeaveRoom:Work",
      "JoinRoom:Work",
    ]);
  });

  it("votes, reads who voted and asks a poll only in the window's profile, as every room call", async () => {
    kithd.remembered = "";
    const polls = () =>
      Promise.all([
        outcome(window, "VotePoll", bytes(backend.votePoll.input, { roomId: "!r:x", eventId: "$p", options: ["0"] })),
        outcome(window, "PollVoters", bytes(backend.pollVoters.input, { roomId: "!r:x", eventId: "$p" })),
        outcome(
          window,
          "CreatePoll",
          bytes(backend.createPoll.input, { roomId: "!r:x", question: "Lunch?", options: ["Soup", "Salad"] }),
        ),
      ]);
    await device();
    expect(await polls()).toEqual(["refused", "refused", "refused"]);
    expect(kithd.reached).toEqual(["Device:"]);
    await gate.call(window, "ChooseProfile", bytes(DeviceService.method.chooseProfile.input, { name: "Home" }));
    expect(await polls()).toEqual(["answered", "answered", "answered"]);
    expect(kithd.reached.slice(2).sort()).toEqual(["CreatePoll:Home", "PollVoters:Home", "VotePoll:Home"]);
    // Not from another frame either.
    const frame: Frame = { url: "https://example.com/", parent: null };
    expect(await outcome(frame, "VotePoll", bytes(backend.votePoll.input, { roomId: "!r:x", eventId: "$p" }))).toBe(
      "refused",
    );
    expect(kithd.reached).toHaveLength(5);
  });

  it("signs out only an account whose own space is on the profile's rail", async () => {
    await device();
    kithd.accountSpaces = ["telegram:1/account"];
    const signOut = (spaceId: string) =>
      outcome(window, "SignOut", bytes(backend.signOut.input, { spaceId, forget: true }));
    expect(await signOut("!friends:x")).toBe("refused"); // a space, not an account's
    expect(await signOut("telegram:2/account")).toBe("refused"); // not on this profile's rail
    expect(await signOut("")).toBe("refused");
    expect(await signOut("telegram:1/account")).toBe("answered");
    expect(kithd.signedOut).toEqual(["telegram:1/account"]);
  });

  it("acts only on a login the window leads, as its step allows, one answer at a time", async () => {
    await device();
    await leading();
    const answer = (login: string, values: Record<string, string> = {}) =>
      outcome(window, "AnswerLogin", bytes(backend.answerLogin.input, { login, values }));
    // Not begun here (the TUI's, or made up), or a value its step does not ask.
    expect(await answer("l9")).toBe("refused");
    expect(await answer("l1", { password: "x" })).toBe("refused");
    // The other way of a step that offers it; the account a step hands out, once.
    expect(await outcome(window, "InsteadLogin", bytes(backend.insteadLogin.input, { login: "l4" }))).toBe("refused");
    const write = () =>
      outcome(
        window,
        "WriteAccount",
        bytes(DeviceService.method.writeAccount.input, { table: "whatsapp.account", values: { name: "l4" } }),
      );
    expect(await write()).toBe("answered");
    expect(await write()).toBe("refused");
    expect(
      await outcome(
        window,
        "WriteAccount",
        bytes(DeviceService.method.writeAccount.input, { table: "whatsapp.account", values: { name: "other" } }),
      ),
    ).toBe("refused");
    // Answered to its end (done), or canceled: the window leads it no more.
    expect(await answer("l1")).toBe("answered");
    expect(await answer("l1")).toBe("refused");
    expect(await outcome(window, "CancelLogin", bytes(backend.cancelLogin.input, { login: "l2" }))).toBe("answered");
    expect(await answer("l2")).toBe("refused");
    expect(await outcome(window, "CancelLogin", bytes(backend.cancelLogin.input, { login: "l2" }))).toBe("refused");
  });

  it("has kithd end the logins the window led when it reloads or closes, in the profile each began in", async () => {
    await device();
    await leading();
    await gate.call(window, "CancelLogin", bytes(backend.cancelLogin.input, { login: "l1" }));
    // A login begun before the reload, answered after it, is nobody's: ended as it arrives.
    kithd.holdBegins = true;
    const late = gate.call(window, "BeginLogin", bytes(backend.beginLogin.input, { network: "whatsapp" }));
    await until(() => kithd.reached.filter((r) => r.startsWith("BeginLogin")).length === 5, "the begin to reach kithd");
    gate.restart();
    kithd.releaseBegins();
    await late;
    await until(() => kithd.canceled.length === 5, "the logins ended");
    expect(kithd.canceled.map((c) => c.login).sort()).toEqual(["l1", "l2", "l3", "l4", "l5"]);
    expect(kithd.canceled.every((c) => c.profile === "Work")).toBe(true);
    // Closing: the logins led since are ended, and none twice.
    kithd.holdBegins = false;
    await device();
    await gate.call(window, "BeginLogin", bytes(backend.beginLogin.input, { network: "whatsapp" }));
    await gate.endLogins();
    await gate.endLogins();
    expect(kithd.canceled.map((c) => c.login)).toEqual([...kithd.canceled.slice(0, 5).map((c) => c.login), "l6"]);
  });

  it("answers a FetchEvent of a message kithd has not got with no message, and every other failure as one", async () => {
    await device();
    kithd.fetched.set("$here", create(MessageSchema, { id: "$here", body: "the question" }));
    kithd.fetched.set("$broken", new ConnectError("cache is locked", Code.Internal));
    const fetch = async (eventId: string) =>
      fromBinary(
        backend.fetchEvent.output,
        await gate.call(window, "FetchEvent", bytes(backend.fetchEvent.input, { roomId: "!r:x", eventId })),
      );
    expect((await fetch("$here")).message?.body).toBe("the question");
    // Gone from its network, or never held: an answer, so main's handler has nothing to log.
    expect((await fetch("$gone")).message).toBeUndefined();
    await expect(fetch("$broken")).rejects.toThrow("cache is locked");
    // Not found from any other call is still a failure: a room gone is no room joined.
    kithd.invited.push("!gone:x");
    kithd.gone.add("!gone:x");
    await expect(
      gate.call(window, "JoinRoom", bytes(backend.joinRoom.input, { roomIdOrAlias: "!gone:x" })),
    ).rejects.toThrow("no room !gone:x");
  });

  it("answers only a verification of this session the window was shown or asked for, in its profile, while it lasts", async () => {
    await device(); // Work
    const shown = listen("Verifications");
    await until(() => kithd.streams(backend.verifications) === 1, "Verifications open");
    const request = (txnId: string, from = "matrix:@me:x") => {
      kithd.sendOn(backend.verifications, { verification: { kind: VerificationKind.REQUESTED, txnId, from } });
    };
    const answer = (name: string, txnId: string) => outcome(window, name, bytes(backend.confirmSAS.input, { txnId }));
    const txns = () =>
      shown.events.flatMap((e) =>
        e.kind === "frames"
          ? e.frames.map((f) => fromBinary(backend.verifications.output, f).verification?.txnId ?? "")
          : [],
      );

    // Your own session's request is shown and answered; someone else's is neither, nor one never shown.
    request("mine");
    request("eve", "matrix:@eve:x");
    request("mine2");
    await until(() => txns().includes("mine2"), "the requests shown");
    expect(txns()).toEqual(["mine", "mine2"]);
    expect(await answer("AcceptVerification", "mine")).toBe("answered");
    expect(await answer("ConfirmSAS", "mine")).toBe("answered");
    expect(await answer("AcceptVerification", "eve")).toBe("refused");
    expect(await answer("CancelVerification", "never")).toBe("refused");
    expect(await answer("ConfirmSAS", "")).toBe("refused");

    // Who you are cannot be read: a request is not shown, rather than shown to anyone.
    kithd.selves = [];
    request("unsure");
    kithd.sendOn(backend.verifications, { verification: { kind: VerificationKind.SAS, txnId: "mine2" } });
    await until(() => txns().length === 3, "the emoji shown");
    expect(txns()).toEqual(["mine", "mine2", "mine2"]);
    expect(await answer("AcceptVerification", "unsure")).toBe("refused");
    kithd.selves = ["matrix:@me:x"];

    // Canceled by the other device (or timed out): a confirm after it is refused.
    kithd.sendOn(backend.verifications, { verification: { kind: VerificationKind.CANCELED, txnId: "mine" } });
    await until(() => txns().length === 4, "the cancel shown");
    expect(await answer("ConfirmSAS", "mine")).toBe("refused");

    // The window's own ask: answered by its transaction.
    await gate.call(window, "StartVerification", bytes(backend.startVerification.input, { on: "matrix:1/" }));
    expect(await answer("CancelVerification", "v1")).toBe("answered");

    // kithd restarting: what it held is forgotten, and so is what the window may answer.
    await kithd.stop();
    await until(() => shown.events.at(-1)?.kind === "detached", "the stream detached");
    await kithd.start();
    await until(() => kithd.streams(backend.verifications) === 1, "Verifications open again");
    expect(await answer("ConfirmSAS", "mine2")).toBe("refused");
    expect(await answer("CancelVerification", "v1")).toBe("refused");

    // Another profile: what was shown or asked for in Work is not answered from Home, an ask
    // that kithd answers only after the window moved included.
    request("work");
    await until(() => txns().includes("work"), "Work's request shown");
    let answerAsk: () => void = () => undefined;
    kithd.verifyHeld = new Promise<void>((resolve) => (answerAsk = resolve));
    const ask = gate.call(window, "StartVerification", bytes(backend.startVerification.input, { on: "matrix:1/" }));
    await gate.call(window, "ChooseProfile", bytes(DeviceService.method.chooseProfile.input, { name: "Home" }));
    answerAsk();
    await ask;
    expect(await answer("AcceptVerification", "work")).toBe("refused");
    expect(await answer("CancelVerification", "v2")).toBe("refused");
    await until(() => kithd.streams(backend.verifications) === 0, "Work's Verifications ended at kithd");

    // A reload: the window loading afresh answers nothing it was shown before.
    const home = listen("Verifications");
    await until(() => kithd.streams(backend.verifications) === 1, "Home's Verifications open");
    request("home");
    await until(() => home.events.some((e) => e.kind === "frames"), "Home's request shown");
    gate.restart();
    await device(); // runs in Home again
    expect(await answer("AcceptVerification", "home")).toBe("refused");

    expect(kithd.verifyAnswers).toEqual([
      "AcceptVerification mine:Work",
      "ConfirmSAS mine:Work",
      "CancelVerification v1:Work",
    ]);
    shown.stop();
    home.stop();
  });

  it("cancels or retries only a queued message it was shown or queued, in the profile it runs in", async () => {
    await device(); // Work
    const on = (name: string, id: string) => outcome(window, name, bytes(backend.cancelScheduled.input, { id }));
    kithd.queue.push("q1");
    // Not shown yet: an ID guessed, or another profile's, never reaches kithd.
    expect(await on("CancelScheduled", "q1")).toBe("refused");
    await gate.call(window, "ScheduledMessages", empty);
    expect(await on("CancelScheduled", "q1")).toBe("answered");
    expect(await on("RetryScheduled", "q1")).toBe("answered");
    expect(await on("RetryScheduled", "q9")).toBe("refused");
    // One it queued itself.
    const queued = fromBinary(
      backend.schedule.output,
      await gate.call(
        window,
        "Schedule",
        bytes(backend.schedule.input, { message: { roomId: "!r:x", body: "later" } }),
      ),
    );
    expect(await on("CancelScheduled", queued.id)).toBe("answered");
    // Left for another profile, or loaded afresh: what it was shown is not its any more,
    // nor what Work's queue answers once the window has left it.
    kithd.queue.push("q7");
    let answer: () => void = () => undefined;
    kithd.queueHeld = new Promise<void>((resolve) => (answer = resolve));
    const late = gate.call(window, "ScheduledMessages", empty);
    await until(() => kithd.reached.filter((r) => r.startsWith("ScheduledMessages")).length === 2, "the read at kithd");
    await gate.call(window, "ChooseProfile", bytes(DeviceService.method.chooseProfile.input, { name: "Home" }));
    answer();
    await late;
    kithd.queueHeld = Promise.resolve();
    expect(await on("CancelScheduled", "q1")).toBe("refused");
    expect(await on("CancelScheduled", "q7")).toBe("refused");
    await gate.call(window, "ScheduledMessages", empty);
    gate.restart();
    await device();
    expect(await on("CancelScheduled", "q1")).toBe("refused");
    expect(kithd.onQueued).toEqual([
      "CancelScheduled q1:Work",
      "RetryScheduled q1:Work",
      `CancelScheduled ${queued.id}:Work`,
    ]);
  });

  it("files a room only through kithd's judgment, in the profile the window ran in when it asked", async () => {
    kithd.profiles = ["Home", "Work"];
    kithd.remembered = "";
    const places = (roomId: string) => outcome(window, "FilingPlaces", bytes(backend.filingPlaces.input, { roomId }));
    const file = (roomId: string) =>
      gate.call(window, "FileRoom", bytes(backend.fileRoom.input, { roomId, tags: [{ tag: "Work", in: true }] }));
    await device();
    expect(await places("!r:x")).toBe("refused"); // no profile yet
    expect(kithd.filings).toEqual([]);
    await gate.call(window, "ChooseProfile", bytes(DeviceService.method.chooseProfile.input, { name: "Home" }));
    expect(await places("!r:x")).toBe("answered");
    // kithd refuses a room the profile does not hold, and the window hears it as kithd's.
    kithd.notHeld.add("!elsewhere:x");
    await expect(file("!elsewhere:x")).rejects.toThrow(/holds no room/);
    // A filing asked in Home stays Home's, the window leaving for Work meanwhile.
    let release: () => void = () => undefined;
    kithd.fileHeld = new Promise((resolve) => {
      release = resolve;
    });
    const filing = file("!r:x");
    await until(() => kithd.filings.includes("FileRoom !r:x:Home"), "the filing to reach kithd");
    await gate.call(window, "ChooseProfile", bytes(DeviceService.method.chooseProfile.input, { name: "Work" }));
    release();
    await filing;
    expect(kithd.filings).toEqual(["FilingPlaces !r:x:Home", "FileRoom !elsewhere:x:Home", "FileRoom !r:x:Home"]);
    // What a filing makes, and the whole config, stay out of the window's reach.
    for (const name of ["AddToSpace", "RemoveFromSpace", "SetRoomArchived", "MarkTag", "UpdateConfig", "GetConfig"]) {
      expect(await outcome(window, name, empty), name).toBe("refused");
    }
  });

  it("runs a command in the window's profile, named by main, and ends it when the window reloads", async () => {
    const run = () =>
      gate.call(window, "RunCommand", bytes(DeviceService.method.runCommand.input, { name: "zoom", roomId: "!r:x" }));
    // Before a profile is chosen it would run in none: refused before the relay.
    expect(await outcome(window, "RunCommand", empty)).toBe("refused");
    expect(await outcome(window, "Commands", empty)).toBe("refused");
    await device();
    await gate.call(window, "Commands", bytes(DeviceService.method.commands.input, { roomId: "!r:x" }));
    expect(kithd.reached.filter((r) => r.startsWith("Commands"))).toEqual(["Commands:Work"]);

    let answer: () => void = () => undefined;
    kithd.ranHeld = new Promise<void>((resolve) => (answer = resolve));
    const running = run();
    await until(() => kithd.runs.length === 1, "the run reaching the relay");
    gate.restart(); // the window loads afresh
    await expect(running).rejects.toThrow();
    await until(() => kithd.killed.length === 1, "the relay told the run ended");
    answer();
    expect(kithd.runs).toEqual(["zoom !r:x:Work"]);
  });

  it("copies a command's output for the clipboard in main, and tells the window only that it did", async () => {
    const copied: string[] = [];
    gate = new Gate(kithd.transport(), (text) => copied.push(text));
    await device();
    const run = async () =>
      fromBinary(
        DeviceService.method.runCommand.output,
        await gate.call(
          window,
          "RunCommand",
          bytes(DeviceService.method.runCommand.input, { name: "zoom", roomId: "!r:x" }),
        ),
      );
    kithd.ran = { output: "clipboard", body: "https://meet.example/abc" };
    const got = await run();
    expect(copied).toEqual(["https://meet.example/abc"]);
    expect([got.output, got.body]).toEqual(["clipboard", ""]);
    kithd.ran = { output: "compose", body: "hello" };
    expect((await run()).body).toBe("hello");
    expect(copied).toHaveLength(1);
  });
});
