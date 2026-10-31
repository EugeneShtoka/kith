// The verifications of this session the window may answer, as main sees them pass the
// gate. kithd shows every client each verification request and its emoji, a request
// from someone else included, and answers whatever transaction a call names. The window
// verifies your own sessions only (as the TUI's :verify does; verifying other people is
// not built), so it is shown a request only when it comes from one of your own IDs
// (Selves), and answers, confirms or calls off only a verification it was shown or
// asked for itself, in the profile it runs in, until kithd says it ended or the window
// loses kithd (a kithd that restarted has forgotten it).
import { fromBinary } from "@bufbuild/protobuf";

import { BackendService, VerificationsResponseSchema } from "../gen/backend/v1/services_pb";
import { VerificationKind } from "../gen/backend/v1/types_pb";
import type { StreamEvent } from "../shared/ipc";

const backend = BackendService.method;

/** A refusal: the window answered a verification it was not shown. */
export class VerifyRefused extends Error {}

/** The calls that answer a verification, by the transaction each names. */
export const answerCalls = new Set([
  backend.acceptVerification.name,
  backend.confirmSAS.name,
  backend.cancelVerification.name,
]);

export class Verifying {
  /** The verifications the window may answer, all in the profile it runs in. */
  private readonly held = new Set<string>();
  /** The profile the window runs in ("" none). */
  private profile = "";

  /** selves is who you are, as kithd says in profile (Selves). */
  constructor(private readonly selves: (profile: string) => Promise<readonly string[]>) {}

  /** admit refuses an answer to txn unless the window may answer it. */
  admit(txn: string): void {
    if (!this.held.has(txn)) {
      throw new VerifyRefused(`kith: the window answers only a verification of this session it was shown, not ${txn}`);
    }
  }

  /**
   * asked holds txn, the verification the window asked for in profile (StartVerification's
   * answer), unless the window has left profile meanwhile.
   */
  asked(txn: string, profile: string): void {
    this.hold(txn, profile);
  }

  /** runIn is the window running in profile: what it held in another is dropped (it left it, or reloaded). */
  runIn(profile: string): void {
    if (profile !== this.profile) {
      this.held.clear();
      this.profile = profile;
    }
  }

  /**
   * stream is post for a Verifications stream opened in profile: its events posted in
   * the order they came, a request from someone else left out, and the window's
   * verifications held as it is shown them and dropped as they end.
   */
  stream(profile: string, post: (event: StreamEvent) => void): (event: StreamEvent) => void {
    let queue = Promise.resolve();
    return (event) => {
      queue = queue.then(() => this.sift(profile, event, post));
    };
  }

  /** hold holds txn, shown or asked for in profile, while the window still runs there. */
  private hold(txn: string, profile: string): void {
    if (txn !== "" && profile === this.profile) {
      this.held.add(txn);
    }
  }

  private async sift(profile: string, event: StreamEvent, post: (event: StreamEvent) => void): Promise<void> {
    if (event.kind === "detached" && profile === this.profile) {
      this.held.clear(); // kithd away: a step sent meanwhile is lost, and a restarted kithd forgot them
    }
    if (event.kind !== "frames") {
      post(event);
      return;
    }
    const kept: Uint8Array[] = [];
    for (const bytes of event.frames) {
      const v = fromBinary(VerificationsResponseSchema, bytes).verification;
      if (v === undefined) {
        continue;
      }
      switch (v.kind) {
        case VerificationKind.REQUESTED:
          if (!(await this.mine(v.from, profile))) {
            continue; // someone else's, which the window does not verify
          }
          this.hold(v.txnId, profile); // not if the window left profile while Selves was asked
          break;
        case VerificationKind.DONE:
        case VerificationKind.CANCELED:
          this.held.delete(v.txnId);
          break;
        default:
          break;
      }
      kept.push(bytes);
    }
    if (kept.length > 0) {
      post({ kind: "frames", frames: kept });
    }
  }

  /** mine: from is one of your own IDs; a lookup that fails says no. */
  private async mine(from: string, profile: string): Promise<boolean> {
    try {
      return from !== "" && (await this.selves(profile)).includes(from);
    } catch {
      return false;
    }
  }
}
