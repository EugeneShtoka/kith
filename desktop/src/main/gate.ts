import { hostname } from "node:os";

import {
  create,
  type DescMessage,
  type DescMethodServerStreaming,
  type DescMethodUnary,
  fromBinary,
  type MessageInitShape,
  type MessageShape,
  toBinary,
} from "@bufbuild/protobuf";
import { Code, ConnectError, type Transport } from "@connectrpc/connect";

import {
  BackendService,
  MessagesResponseSchema,
  SeatHolderSchema,
  type SeatRequestSchema,
} from "../gen/backend/v1/services_pb";
import { DeviceService } from "../gen/device/v1/device_pb";
import type { StreamEvent } from "../shared/ipc";
import { runIn } from "../shared/profile";
import { LoginRefused, loginCalls, Logins, type Ticket } from "./logins";
import { type Asks, type Join, PairingRefused, Pairings } from "./pairings";
import { keepOpen, openOnce } from "./streams";
import { answerCalls, VerifyRefused, Verifying } from "./verifications";

const backend = BackendService.method;
const device = DeviceService.method;

/** refuseAll answers no to everything: a gate given no way to ask the person pairs, unpairs and joins nothing. */
const refuseAll: Asks = {
  pair: () => Promise.resolve(false),
  unpair: () => Promise.resolve(false),
  join: () => Promise.resolve(false),
};

/**
 * The calls and streams the window may use, by name. An XSS in the window holds whatever
 * the preload holds, so this list is its scope: it grows with features, and a call that
 * reaches beyond reading and writing messages joins only with the PR that needs it.
 */
const calls = new Map<string, DescMethodUnary>(
  [
    backend.status,
    backend.rail,
    backend.rooms,
    backend.lastMessages,
    backend.cachedUnread,
    backend.cachedInvites,
    backend.joinRoom,
    backend.leaveRoom,
    backend.cachedTimeline,
    backend.timeline,
    backend.messagesAround,
    backend.fetchEvent,
    backend.cachedReactions,
    backend.markRead,
    // The composer: sending, editing and deleting, reactions, typing, mentions, the
    // emoji ranking, and drafts (written as the seat's, below).
    backend.send,
    backend.redact,
    backend.sendReaction,
    backend.sendTyping,
    backend.mentionCandidates,
    backend.emojiScores,
    backend.recordEmoji,
    backend.selves,
    backend.drafts,
    backend.replaceDraft,
    // Polls: a vote, who voted, and a poll asked.
    backend.votePoll,
    backend.pollVoters,
    backend.createPoll,
    // Search: kithd answers it in the profile's rooms only.
    backend.searchMessages,
    // Threads: a thread's older replies, and its receipt.
    backend.threadPage,
    backend.markThreadRead,
    // Links: the room open, which kithd hands a clicked link by (as the seat, below).
    backend.present,
    device.device,
    device.chooseProfile,
    device.deviceSettings,
    // The settings page: device.toml written only over the file it read.
    device.updateDeviceSettings,
    // The theme: read, and its keys written over the files the window read; a room's
    // remembered sender colors, which it draws people in by default.
    device.theme,
    device.updateTheme,
    backend.senderSlots,
    // Accounts: the networks to log in to, and a login the window leads (logins.ts: only
    // its own, as its step allows), its new account written and its QR code encoded by
    // the relay; and signing out an account whose own space is on the profile's rail.
    backend.loginNetworks,
    backend.beginLogin,
    backend.answerLogin,
    backend.cancelLogin,
    backend.insteadLogin,
    device.writeAccount,
    device.qRCode,
    backend.signOut,
    // Verifying this session (verifications.ts: only one the window was shown or asked
    // for), from one of the accounts it reads.
    backend.accounts,
    backend.startVerification,
    backend.acceptVerification,
    backend.confirmSAS,
    backend.cancelVerification,
    // Spelling and completion: the draft checked, a word taught to the dictionary, the
    // word being typed finished, and the next word asked of the completion model alone
    // (ModelTask "complete", never a task that sends a conversation to [assist]'s
    // endpoint). What is installed is only what kithd offered the window (installs).
    backend.checkSpelling,
    backend.learnWord,
    backend.allowRareWord,
    backend.completeWord,
    backend.modelTask,
    backend.detectLanguages,
    backend.detectModel,
    backend.installDictionary,
    backend.installFrequencies,
    backend.installModel,
    // Sending later: a message queued, the queue (kithd answers the profile's rooms only),
    // and one canceled or tried again (only one the window was shown or queued, here).
    backend.schedule,
    backend.scheduledMessages,
    backend.cancelScheduled,
    backend.retryScheduled,
    // The commands of the device's commands directory, listed and run by the relay: by
    // name, in a room of the window's profile, the program given only what the relay
    // looks up (kith-relay's RunCommand). Output for the clipboard main copies itself.
    device.commands,
    device.runCommand,
    // Filing a room into tags and spaces: where it can go, and filing it, kithd judging
    // each room, tag and space named against the profile the gate names and writing the
    // tags into the config itself, so the window is given no config to read or write.
    // AddToSpace, RemoveFromSpace, SetRoomArchived and MarkTag stay off the list: kithd
    // makes them for a filing it judged.
    backend.filingPlaces,
    backend.fileRoom,
    // People and their cards: kithd's phonebook folder, read and written by kithd. The
    // window names a person by an ID, a card People listed (by its name, which kithd
    // looks up among the folder's cards) or a DM; it never names a file nor gets a path,
    // writes one key over the card as it read it, and sees only whom the profile shows.
    backend.people,
    backend.personCard,
    backend.setPersonField,
    backend.personKeys,
    // The bridge's settings, one at a time: kithd lists those the window may set and sets
    // one over the file as the window read it (SetConfigSetting at its revision). Which
    // ones is kithd's list (configsettings.go): never a program, a path, an address, an
    // account, a secret, the log, or what an assistant may reach. The window is given
    // neither the config's text (ConfigChanged's is taken out, below) nor a way to write
    // it whole.
    backend.configSettings,
    backend.setConfigSetting,
    // Pairing (pairings.ts): the devices paired, a pairing's ticket written as the URI a
    // new device joins with, and the two calls that change who is paired, each sent only
    // once the person said yes in main's own dialog: confirming a pairing the window
    // opened (StartPairing, below) and unpairing a device it was shown listed. AddDevice
    // (pairing by a pasted ID, with no digits to compare) stays off.
    backend.devices,
    backend.confirmPairing,
    backend.removeDevice,
    device.pairingURI,
  ].map((m) => [m.name, m]),
);

/**
 * The streams opened once, never again after they end: a pairing (StartPairing: its
 * token lives as long as the stream) and a join (JoinBridge, the relay's, opened by main
 * only once the person said yes in its dialog to the bridge the URI names).
 */
const onceStreams = new Map<string, DescMethodServerStreaming>(
  [backend.startPairing, device.joinBridge].map((m) => [m.name, m]),
);

/** The calls on one queued message, by its ID: only one the window was shown or queued. */
const onQueued = new Set<string>([backend.cancelScheduled.name, backend.retryScheduled.name]);

/** The relay's calls that run in the window's profile, which main names on them as on kithd's. */
const profiledDevice = new Set<string>([device.commands.name, device.runCommand.name]);

/** The calls that install a download, by the offer that names what they may install. */
const installs = new Map<string, string>([
  [backend.installDictionary.name, backend.detectLanguages.name],
  [backend.installFrequencies.name, backend.detectLanguages.name],
  [backend.installModel.name, backend.detectModel.name],
]);

/** modelComplete is the one ModelTask the window may ask: the next word (domain.ModelComplete). */
const modelComplete = "complete";
const streams = new Map<string, DescMethodServerStreaming>(
  [
    backend.changed,
    backend.configChanged,
    backend.seat,
    backend.unreadStream,
    backend.invites,
    backend.messages,
    backend.reactions,
    backend.followStream,
    backend.verifications,
  ].map((m) => [m.name, m]),
);

/**
 * The calls the window may make only for a room the profile has an invitation to, by
 * the room each names: accepting (JoinRoom) and declining (LeaveRoom) one is all it
 * does with them, so a window run by someone else's message can neither join nor leave
 * any other room.
 */
const invitedOnly = new Map<string, (request: unknown) => string>([
  [backend.joinRoom.name, (r) => (r as MessageShape<typeof backend.joinRoom.input>).roomIdOrAlias],
  [backend.leaveRoom.name, (r) => (r as MessageShape<typeof backend.leaveRoom.input>).roomId],
]);

/** How long a login the window no longer leads is given to end in kithd (the TUI's wait). */
const cancelWait = 5000;

/** The window's origin: the app:// protocol main serves it from. */
export const appURL = "app://kith/";

/** What the gate reads of the frame a message came from (Electron's WebFrameMain). */
export interface Frame {
  url: string;
  parent: unknown;
}

/** A refusal: the window asked for what it may not have. */
export class Refused extends Error {}

/**
 * Gate is all the window reaches of kithd: the calls and streams on the list, from the
 * window's own top frame, with requests that decode as their method's. Every request to
 * kithd carries the profile the window runs in, which the gate follows from the relay's
 * answers (runIn), and none is sent before there is one: kithd would answer it for the
 * config's first profile. A stream opened in one profile is ended when the window
 * leaves it.
 */
export class Gate {
  private profile = "";
  private readonly open = new Set<{ profile: string; stop: () => void; post: (event: StreamEvent) => void }>();
  /** The client each Seat stream open asks the seat for, in its profile. */
  private readonly seats = new Map<object, { profile: string; client: string }>();
  /** The logins the window leads; kithd ends one the window no longer does. */
  private readonly logins = new Logins((login, header) =>
    this.transport
      .unary(
        backend.cancelLogin,
        AbortSignal.timeout(cancelWait),
        undefined,
        header,
        create(backend.cancelLogin.input, { login }),
      )
      .then(() => undefined),
  );

  /** What kithd last offered the window to install, by the call installing it. */
  private readonly offered = new Map<string, Set<string>>();
  /** The verifications the window may answer. */
  private readonly verifying = new Verifying(async (profile) => {
    const method = backend.selves;
    const r = await this.transport.unary(
      method,
      undefined,
      undefined,
      { "Kith-Profile": profile },
      create(method.input),
    );
    return r.message.ids;
  });

  /** The queued messages' IDs the window was shown or queued, in the profile it runs in. */
  private queued = new Set<string>();
  /** The commands running, ended when the window loads afresh or closes. */
  private readonly running = new Set<AbortController>();

  /** The pairings the window opened, the devices it was shown, and a join under way. */
  private readonly pairings: Pairings;

  /**
   * copy puts a command's output on the clipboard: its block's `output = "clipboard"`.
   * asks asks the person, in main's own dialogs, before a pairing is confirmed, a device
   * unpaired or a bridge joined (none asked: each is refused). rejoined is the device
   * file perhaps naming a bridge since a join: the relay is to be started again.
   */
  constructor(
    private transport: Transport,
    private readonly copy: (text: string) => void = () => undefined,
    asks: Asks = refuseAll,
    private readonly rejoined: () => void = () => undefined,
  ) {
    this.pairings = new Pairings(asks);
  }

  /**
   * retarget sends every call from now on through transport: a relay started again. A
   * stream open now stays where it was, so the window is loaded afresh after it.
   */
  retarget(transport: Transport): void {
    this.transport = transport;
  }

  /** call makes the call named and answers the response's bytes. */
  async call(frame: Frame | null, name: unknown, request: unknown): Promise<Uint8Array> {
    const method = admit(frame, calls, name);
    const input = decode(method, request);
    const header = this.header(method);
    const profile = this.profile;
    const invited = invitedOnly.get(method.name);
    if (invited !== undefined) {
      await this.mustBeInvited(invited(input), header);
    }
    if (method.name === backend.replaceDraft.name || method.name === backend.present.name) {
      this.asTheSeat(input as MessageShape<typeof backend.replaceDraft.input | typeof backend.present.input>);
    }
    if (method.name === backend.signOut.name) {
      await this.mustBeAnAccount((input as MessageShape<typeof backend.signOut.input>).spaceId, header);
    }
    if (answerCalls.has(method.name)) {
      this.admitAnswer((input as MessageShape<typeof backend.confirmSAS.input>).txnId);
    }
    if (installs.has(method.name)) {
      this.mustBeOffered(method.name, (input as MessageShape<typeof backend.installDictionary.input>).tag);
    }
    if (method.name === backend.modelTask.name) {
      const task = input as MessageShape<typeof backend.modelTask.input>;
      if (task.task !== modelComplete || task.dryRun) {
        throw new Refused(`kith: the window asks the model only for the next word, not ${task.task}`);
      }
    }
    if (onQueued.has(method.name)) {
      this.mustBeQueued((input as MessageShape<typeof backend.cancelScheduled.input>).id);
    }
    if (method.name === backend.confirmPairing.name) {
      const r = input as MessageShape<typeof backend.confirmPairing.input>;
      const m = backend.confirmPairing;
      const answer = await this.asPairings(() =>
        this.pairings.confirm(r.token, r.accept, (accept) =>
          this.transport
            .unary(m, undefined, undefined, header, create(m.input, { token: r.token, accept }))
            .then((res) => res.message),
        ),
      );
      return toBinary(m.output, answer);
    }
    if (method.name === backend.removeDevice.name) {
      const r = input as MessageShape<typeof backend.removeDevice.input>;
      const m = backend.removeDevice;
      const answer = await this.asPairings(() =>
        this.pairings.remove(
          r.id,
          () => this.thisDevice(),
          () =>
            this.transport
              .unary(m, undefined, undefined, header, create(m.input, { id: r.id }))
              .then((res) => res.message),
        ),
      );
      return toBinary(m.output, answer);
    }
    const ticket = loginCalls.has(method.name) ? this.admitLogin(method.name, input, header) : undefined;
    const run = method.name === device.runCommand.name ? new AbortController() : undefined;
    if (run !== undefined) {
      this.running.add(run);
    }
    const response = await this.transport.unary(method, run?.signal, undefined, header, input).catch((err: unknown) => {
      if (ticket !== undefined) {
        this.logins.heard(ticket, undefined, err);
      }
      if (run !== undefined) {
        this.running.delete(run);
      }
      // A message gone from its network, or one kithd holds no copy of, is an answer: the
      // window is answered no message, and its quote shows as not loaded. A rejection
      // here is logged by Electron as an error.
      if (method.name === backend.fetchEvent.name && ConnectError.from(err).code === Code.NotFound) {
        return undefined;
      }
      throw err;
    });
    if (response === undefined) {
      return toBinary(method.output, create(method.output));
    }
    if (ticket !== undefined) {
      this.logins.heard(ticket, response.message);
    }
    if (method.name === backend.startVerification.name) {
      this.verifying.asked((response.message as MessageShape<typeof backend.startVerification.output>).txnId, profile);
    }
    this.offer(method, response.message);
    this.follow(method, response.message);
    this.shown(method, response.message, profile);
    if (method.name === backend.devices.name) {
      this.pairings.devices((response.message as MessageShape<typeof backend.devices.output>).devices, profile);
    }
    if (run !== undefined) {
      this.running.delete(run);
      const ran = response.message as MessageShape<typeof device.runCommand.output>;
      if (ran.output === "clipboard" && ran.body !== "") {
        this.copy(ran.body);
        ran.body = ""; // copied here: the window is told it was, not given it
      }
    }
    return toBinary(method.output, response.message);
  }

  /** subscribe keeps the stream named open, posting its events, until the stop it returns. */
  subscribe(frame: Frame | null, name: unknown, request: unknown, post: (event: StreamEvent) => void): () => void {
    if (typeof name === "string" && onceStreams.has(name)) {
      return this.subscribeOnce(frame, name, request, post);
    }
    const method = admit(frame, streams, name);
    const input = decode(method, request);
    const header = this.header(method);
    let again = () => input;
    if (method.name === backend.followStream.name) {
      this.asTheSeat(input as MessageShape<typeof backend.followStream.input>);
    }
    if (method.name === backend.seat.name) {
      // The seat is asked for from this machine, whatever the window says; and a take by
      // force is forced once: opened again (kithd restarted), it is asked for, so a
      // window that took it meanwhile keeps it (daemon.Remote.keepSeat).
      const seat = input as MessageShape<typeof SeatRequestSchema>;
      seat.where = create(SeatHolderSchema, {
        pid: BigInt(process.pid),
        host: hostname(),
        sinceUnixMs: seat.where?.sinceUnixMs ?? 0n,
      });
      const asked: MessageShape<typeof SeatRequestSchema> = { ...seat, force: false };
      again = () => asked;
    }
    const seated = method.name === backend.seat.name ? {} : undefined;
    if (seated !== undefined) {
      this.seats.set(seated, {
        profile: this.profile,
        client: (input as MessageShape<typeof SeatRequestSchema>).client,
      });
    }
    if (method.name === backend.messages.name) {
      // Opened again (kithd restarted, the relay failed), the stream resumes after the
      // last change posted to the window, so it is sent what it missed meanwhile, once
      // (design-replay.md). A window loading afresh subscribes afresh, from the newest.
      let at: MessageShape<typeof backend.messages.input> | undefined;
      again = () => at ?? input;
      post = resuming(post, (epoch, change) => {
        at = create(backend.messages.input, { sinceEpoch: epoch, sinceChange: change });
      });
    }
    if (method.name === backend.verifications.name) {
      post = this.verifying.stream(this.profile, post);
    }
    if (method.name === backend.configChanged.name) {
      post = withoutConfig(post);
    }
    const stop = keepOpen(this.transport, method, input, header, post, again);
    const held = {
      profile: this.profile,
      stop: () => {
        if (seated !== undefined) {
          this.seats.delete(seated);
        }
        stop();
      },
      post,
    };
    this.open.add(held);
    return () => {
      this.open.delete(held);
      held.stop();
    };
  }

  /**
   * own makes a call main needs for the window (an attachment's upload, readying the
   * file), in the window's profile; the window cannot ask for these itself.
   */
  async own<I extends DescMessage, O extends DescMessage>(
    method: DescMethodUnary<I, O>,
    input: MessageInitShape<I>,
    signal: AbortSignal,
  ): Promise<MessageShape<O>> {
    const header = this.header(method);
    const response = await this.transport.unary(method, signal, undefined, header, create(method.input, input));
    return response.message;
  }

  /**
   * media is the bytes of a message's attachment, in the window's profile (LoadImage):
   * what kith-media:// pictures and saved files are. The window does not call LoadImage
   * itself; main does, for it.
   */
  async media(room: string, event: string): Promise<Uint8Array> {
    const method = backend.loadImage;
    const request = create(method.input, { roomId: room, eventId: event });
    const response = await this.transport.unary(method, undefined, undefined, this.header(method), request);
    return response.message.data;
  }

  /**
   * restart is the window loading afresh (a reload): it runs in no profile until it
   * reads the device again, as a window just started does, so both decide alike.
   */
  restart(): void {
    this.use("");
    void this.logins.restart();
    this.endCommands();
    this.pairings.restart();
  }

  /**
   * subscribeOnce opens a stream that is never opened again: a pairing, whose token lives
   * as long as it does, or a join, opened only once the person said yes in main's dialog.
   */
  private subscribeOnce(frame: Frame | null, name: string, request: unknown, post: (event: StreamEvent) => void) {
    const method = admit(frame, onceStreams, name);
    const input = decode(method, request);
    const header = this.header(method);
    if (method.name === device.joinBridge.name) {
      return this.joinBridge(input as MessageShape<typeof device.joinBridge.input>, post);
    }
    const m = backend.startPairing;
    let stop: () => void = () => undefined;
    const run = this.asPairingsNow(() =>
      this.pairings.opened(() => {
        stop();
      }),
    );
    const held = {
      profile: this.profile,
      stop: () => {
        this.pairings.end(run);
        stop(); // answered already, it is still cut: nothing of it reaches the window after
      },
      post,
    };
    stop = openOnce(this.transport, m, input as MessageShape<typeof m.input>, header, (event) => {
      if (event.kind === "frames") {
        for (const bytes of event.frames) {
          this.pairings.frame(run, fromBinary(m.output, bytes));
        }
      }
      if (event.kind === "ended") {
        this.pairings.end(run);
        this.open.delete(held);
      }
      post(event);
    });
    this.open.add(held);
    return () => {
      this.open.delete(held);
      held.stop();
    };
  }

  /**
   * joinBridge is a join the window asks for: main reads the URI (the relay's Invite),
   * shows the person the bridge it names, and only on their yes opens the relay's
   * JoinBridge. Once the device file may name the bridge (joined, or cut after the digits
   * came), rejoined has the relay started again.
   */
  private joinBridge(input: MessageShape<typeof device.joinBridge.input>, post: (event: StreamEvent) => void) {
    const m = device.joinBridge;
    let gone = false;
    let join: Join | undefined;
    const read = () =>
      this.transport
        .unary(device.invite, undefined, undefined, {}, create(device.invite.input, { uri: input.uri }))
        .then((r) => r.message);
    this.pairings.join(read).then(
      (j) => {
        join = j;
        if (gone) {
          j.stop(); // the window closed it while the person was asked
          return;
        }
        const stop = openOnce(this.transport, m, input, {}, (event) => {
          if (event.kind === "frames") {
            for (const bytes of event.frames) {
              this.pairings.joinFrame(j, fromBinary(m.output, bytes));
            }
          }
          post(event);
          if (event.kind === "ended" && this.pairings.joinEnded(j, false)) {
            this.rejoined();
          }
        });
        j.stop = () => {
          stop();
          if (this.pairings.joinEnded(j, true)) {
            this.rejoined();
          }
        };
      },
      (err: unknown) => {
        if (!gone) {
          post({ kind: "refused", error: err instanceof Error ? err.message : String(err) });
        }
      },
    );
    return () => {
      gone = true;
      join?.stop();
    };
  }

  /** thisDevice is the ID this device's bridge pins it by ("" for kithd's own machine). */
  private async thisDevice(): Promise<string> {
    const m = device.thisDevice;
    const r = await this.transport.unary(m, undefined, undefined, {}, create(m.input));
    return r.message.id;
  }

  /** asPairings is what pairings answers, a refusal of its read as the gate's. */
  private async asPairings<R>(f: () => Promise<R>): Promise<R> {
    try {
      return await f();
    } catch (err) {
      throw err instanceof PairingRefused ? new Refused(err.message) : err;
    }
  }

  private asPairingsNow<R>(f: () => R): R {
    try {
      return f();
    } catch (err) {
      throw err instanceof PairingRefused ? new Refused(err.message) : err;
    }
  }

  /** endCommands ends every command the window runs: the relay kills its program. */
  endCommands(): void {
    for (const run of this.running) {
      run.abort();
    }
    this.running.clear();
  }

  /** endLogins has kithd end every login the window leads: it is closing. */
  endLogins(): Promise<void> {
    return this.logins.end();
  }

  /** header is what a request for method carries: kithd's the window's profile, refused without one. */
  private header(method: DescMethodUnary | DescMethodServerStreaming): HeadersInit {
    if (method.parent.typeName === DeviceService.typeName && !profiledDevice.has(method.name)) {
      return {}; // the relay's own, in no profile
    }
    if (this.profile === "") {
      throw new Refused(`kith: no profile is chosen yet, so ${method.name} would run in the config's first`);
    }
    return { "Kith-Profile": this.profile };
  }

  /** follow moves the window to the profile a DeviceService answer leaves it in. */
  private follow(method: DescMethodUnary, message: unknown): void {
    if (method.name === device.device.name) {
      const d = message as MessageShape<typeof device.device.output>;
      this.use(runIn(this.profile, d.profile, d.profiles));
    } else if (method.name === device.chooseProfile.name) {
      this.use((message as MessageShape<typeof device.chooseProfile.output>).profile);
    }
  }

  /** use runs the window in profile from now on, ending every stream opened in another. */
  private use(profile: string): void {
    if (profile === this.profile) {
      return;
    }
    this.profile = profile;
    this.verifying.runIn(profile);
    this.pairings.runIn(profile);
    this.queued = new Set();
    for (const held of this.open) {
      if (held.profile !== profile) {
        this.open.delete(held);
        held.stop();
        held.post({ kind: "refused", error: `kith: the window left the profile ${held.profile}` });
      }
    }
  }

  /**
   * asTheSeat names the window's seat in this profile on a draft write, whatever the
   * window says: kithd refuses it once another window has the seat (daemon seats.write),
   * so a window that stepped aside never writes over the one that took over. A window
   * asking for no seat here writes none. Its links are listened for, and what it has
   * open told, as the seat too: the window cannot speak for another.
   */
  private asTheSeat(request: { client: string }): void {
    const seat = [...this.seats.values()].find((s) => s.profile === this.profile);
    if (seat === undefined || seat.client === "") {
      throw new Refused(`kith: the window speaks only as its seat in ${this.profile}`);
    }
    request.client = seat.client;
  }

  /**
   * shown keeps the IDs of the queued messages kithd showed the window, or queued for it,
   * in profile: the ones it may cancel or try again while it still runs there.
   */
  private shown(method: DescMethodUnary, message: unknown, profile: string): void {
    if (profile !== this.profile) {
      return; // answered after the window left the profile it asked in
    }
    if (method.name === backend.scheduledMessages.name) {
      for (const m of (message as MessageShape<typeof backend.scheduledMessages.output>).messages) {
        this.queued.add(m.id);
      }
    } else if (method.name === backend.schedule.name) {
      this.queued.add((message as MessageShape<typeof backend.schedule.output>).id);
    }
  }

  /** mustBeQueued refuses a call on a queued message the window was not shown, or queued, here. */
  private mustBeQueued(id: string): void {
    if (!this.queued.has(id)) {
      throw new Refused(`kith: the window cancels or retries only a queued message it was shown here, not ${id}`);
    }
  }

  /** offer keeps what kithd's last offer of each kind names, which the window may install. */
  private offer(method: DescMethodUnary, message: unknown): void {
    if (method.name === backend.detectLanguages.name) {
      const r = message as MessageShape<typeof backend.detectLanguages.output>;
      this.offered.set(backend.installDictionary.name, new Set(r.candidates.map((c) => c.tag)));
      this.offered.set(backend.installFrequencies.name, new Set(r.frequencies.map((c) => c.tag)));
    } else if (method.name === backend.detectModel.name) {
      const r = message as MessageShape<typeof backend.detectModel.output>;
      this.offered.set(backend.installModel.name, new Set(r.offer && r.candidate ? [r.candidate.tag] : []));
    }
  }

  /** mustBeOffered refuses to install a tag kithd's last offer did not name: a download is the person's choice. */
  private mustBeOffered(install: string, tag: string): void {
    if (!(this.offered.get(install)?.has(tag) ?? false)) {
      throw new Refused(
        `kith: the window installs only what kithd offered it (${installs.get(install) ?? ""}), not ${tag}`,
      );
    }
  }

  /** admitAnswer refuses an answer to a verification the window may not answer here. */
  private admitAnswer(txn: string): void {
    try {
      this.verifying.admit(txn);
    } catch (err) {
      throw err instanceof VerifyRefused ? new Refused(err.message) : err;
    }
  }

  /** admitLogin is the ticket for a call of a login, refused unless the window leads it and its step allows the call. */
  private admitLogin(name: string, input: unknown, header: HeadersInit): Ticket {
    try {
      return this.logins.admit(name, input, header);
    } catch (err) {
      throw err instanceof LoginRefused ? new Refused(err.message) : err;
    }
  }

  /**
   * mustBeAnAccount refuses unless space is an account's own space on the profile's rail
   * (one the TUI signs out of by leaving it): kithd signs out whatever account space it is
   * named, in any profile.
   */
  private async mustBeAnAccount(space: string, header: HeadersInit): Promise<void> {
    const rail = await this.transport.unary(backend.rail, undefined, undefined, header, create(backend.rail.input));
    if (space === "" || !rail.message.rows.some((r) => r.space === space && r.owner !== "")) {
      throw new Refused(`kith: the window signs out only an account on its rail, not ${space}`);
    }
  }

  /** mustBeInvited refuses unless the profile has an invitation to room now. */
  private async mustBeInvited(room: string, header: HeadersInit): Promise<void> {
    const method = backend.cachedInvites;
    const invites = await this.transport.unary(method, undefined, undefined, header, create(method.input));
    if (room === "" || !invites.message.invites.some((r) => r.id === room)) {
      throw new Refused(`kith: the window may join or leave only a room it is invited to, not ${room}`);
    }
  }
}

/**
 * resuming posts a Messages stream's frames, telling held each one's position as it is
 * posted (the window holds it from then on). A frame saying to read again is posted as a
 * reload, in its place among the frames.
 */
function resuming(post: (event: StreamEvent) => void, held: (epoch: string, change: bigint) => void) {
  return (event: StreamEvent) => {
    if (event.kind !== "frames") {
      post(event);
      return;
    }
    let run: Uint8Array[] = [];
    for (const bytes of event.frames) {
      const frame = fromBinary(MessagesResponseSchema, bytes);
      if (frame.readAgain) {
        if (run.length > 0) {
          post({ kind: "frames", frames: run });
          run = [];
        }
        post({ kind: "reload" });
      } else {
        run.push(bytes);
      }
      held(frame.epoch, frame.change);
    }
    if (run.length > 0) {
      post({ kind: "frames", frames: run });
    }
  };
}

/**
 * withoutConfig posts ConfigChanged's frames with the config's text taken out: the
 * window is told the config changed, and its revision, never what it holds.
 */
function withoutConfig(post: (event: StreamEvent) => void) {
  return (event: StreamEvent) => {
    if (event.kind !== "frames") {
      post(event);
      return;
    }
    const m = backend.configChanged;
    post({
      kind: "frames",
      frames: event.frames.map((bytes) =>
        toBinary(m.output, create(m.output, { revision: fromBinary(m.output, bytes).revision })),
      ),
    });
  };
}

/** admit is the method named, if the window may use it from frame. */
function admit<M>(frame: Frame | null, allowed: Map<string, M>, name: unknown): M {
  if (!fromTheWindow(frame)) {
    throw new Refused(`kith: a request from ${frame?.url ?? "a closed frame"}, not the window`);
  }
  const method = typeof name === "string" ? allowed.get(name) : undefined;
  if (method === undefined) {
    throw new Refused(`kith: the window may not use ${String(name)}`);
  }
  return method;
}

/** fromTheWindow: the frame is the window's top frame, on its own origin. */
export function fromTheWindow(frame: Frame | null): boolean {
  if (frame === null || frame.parent !== null) {
    return false;
  }
  try {
    const url = new URL(frame.url);
    return url.protocol === "app:" && url.host === "kith";
  } catch {
    return false;
  }
}

/**
 * decode is request as method's input, refused unless it is bytes that decode as one.
 * Fields the input does not have are dropped, so kithd is sent the method's fields alone.
 */
function decode(method: DescMethodUnary | DescMethodServerStreaming, request: unknown) {
  if (!(request instanceof Uint8Array)) {
    throw new Refused(`kith: the ${method.name} request is not bytes`);
  }
  try {
    return fromBinary(method.input, request, { readUnknownFields: false });
  } catch (err) {
    throw new Refused(`kith: the ${method.name} request does not decode: ${String(err)}`);
  }
}
