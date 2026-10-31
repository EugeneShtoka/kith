// Verifying this session against another of your own: the TUI's :verify overlay
// (internal/tui handleVerify, handleVerifyKey) as state, outside React. Every client of
// kithd is shown each request and its emoji; kithd takes one answer to each step and
// tells every client it was given (ACCEPTED, CONFIRMED).
import type { Account } from "../gen/backend/v1/services_pb";
import { type SASEmoji, type Verification, VerificationKind } from "../gen/backend/v1/types_pb";

/** The verification shown: a request, then the emoji to compare. */
export interface Flow {
  txn: string;
  /** This window asked: the other session accepts, this one can only call it off. */
  ours: boolean;
  stage: "requested" | "sas";
  /** Who asks, from which session (a request from elsewhere). */
  from: string;
  device: string;
  /** What to compare: the emoji, or the numbers where there are none. */
  emojis: readonly SASEmoji[];
  decimals: readonly bigint[];
  /** This window's answer to the step shown is on its way. */
  sending: boolean;
  /** The step shown was answered: by this window (mine), or by another. */
  answered: boolean;
  mine: boolean;
  /** Why this window's last answer failed ("" none). */
  failed: string;
}

/** The window's verification: the one shown, and what the last ones came to. */
export interface Verify {
  flow: Flow | undefined;
  /** What verifications came to, oldest first, until another begins or it is put away. */
  said: readonly string[];
  /** The window is asking its other sessions (StartVerification is on its way). */
  asking: boolean;
}

export const noVerify: Verify = { flow: undefined, said: [], asking: false };

/** The answer kithd tells of for each stage. */
const answerTo = { requested: VerificationKind.ACCEPTED, sas: VerificationKind.CONFIRMED } as const;

function flow(txn: string, ours: boolean, from = "", device = ""): Flow {
  return {
    txn,
    ours,
    stage: "requested",
    from,
    device,
    emojis: [],
    decimals: [],
    sending: false,
    answered: false,
    mine: false,
    failed: "",
  };
}

/** stepped is v after a step kithd streamed; a step of a verification not shown changes nothing. */
export function stepped(v: Verify, s: Verification): Verify {
  const f = v.flow?.txn === s.txnId ? v.flow : undefined;
  switch (s.kind) {
    case VerificationKind.REQUESTED:
      if (f?.ours === true) {
        return v; // our own request, echoed
      }
      return { ...v, flow: flow(s.txnId, false, s.from, s.device), said: [] };
    case VerificationKind.SAS:
      if (f === undefined) {
        return v;
      }
      return {
        ...v,
        flow: {
          ...f,
          stage: "sas",
          emojis: s.emojis,
          decimals: s.decimals,
          sending: false,
          answered: false,
          mine: false,
          failed: "",
        },
      };
    case VerificationKind.ACCEPTED:
    case VerificationKind.CONFIRMED:
      // An answer to an earlier step (the acceptance arriving after the emoji) leaves the step shown.
      return f !== undefined && answerTo[f.stage] === s.kind ? { ...v, flow: { ...f, answered: true } } : v;
    case VerificationKind.DONE:
      return f === undefined ? v : { ...v, flow: undefined, said: ["This session is verified."] };
    case VerificationKind.CANCELED:
      return f === undefined ? v : { ...v, flow: undefined, said: [`Verification canceled: ${s.reason}`] };
    case VerificationKind.RESTORED:
      return { ...v, said: [...v.said, s.reason] };
    case VerificationKind.UNSPECIFIED:
      return v;
  }
}

/** answerable: the step shown may be answered from here now. */
export function answerable(f: Flow | undefined): f is Flow {
  return f !== undefined && !f.sending && !f.answered && !(f.ours && f.stage === "requested");
}

/** asking is v with this window asking its other sessions to verify it. */
export function asking(v: Verify): Verify {
  return { ...v, asking: true, said: [] };
}

/** asked is v once kithd answered the ask with its transaction: the other session accepts. */
export function asked(v: Verify, txn: string): Verify {
  return { flow: flow(txn, true), said: [], asking: false };
}

/** askFailed is v once the ask failed. */
export function askFailed(v: Verify, error: string): Verify {
  return { ...v, asking: false, said: [`Could not ask: ${error}`] };
}

/** answering is v with this window's answer to the step shown on its way. */
export function answering(v: Verify): Verify {
  return v.flow === undefined ? v : { ...v, flow: { ...v.flow, sending: true, mine: true, failed: "" } };
}

/**
 * heard is v once this window's answer to stage of txn came back: given (error
 * "") or not. One that comes back after its verification ended, or moved on, changes
 * nothing; one refused because another window answered first leaves that answer
 * standing.
 */
export function heard(v: Verify, txn: string, stage: Flow["stage"], error: string): Verify {
  const f = v.flow;
  if (f?.txn !== txn || f.stage !== stage) {
    return v;
  }
  if (error === "") {
    return { ...v, flow: { ...f, sending: false, answered: true, mine: true } };
  }
  return { ...v, flow: { ...f, sending: false, mine: false, failed: f.answered ? "" : error } };
}

/** canceled is v once this window called the one shown off, as the TUI does at once. */
export function canceled(v: Verify): Verify {
  return v.flow === undefined ? v : { ...v, flow: undefined, said: ["Verification canceled."] };
}

/**
 * lost is v once the window lost kithd (its stream ended): a step sent meanwhile is not
 * sent again, and a kithd that restarted forgot the verification, so the one shown ends.
 */
export function lost(v: Verify): Verify {
  return v.flow === undefined
    ? v
    : {
        ...v,
        flow: undefined,
        said: ["The link to kithd was lost mid-verification: start it again if it did not finish."],
      };
}

/** dismissed is v with what the last verifications came to put away. */
export function dismissed(v: Verify): Verify {
  return { ...v, said: [] };
}

/**
 * verifyAs is the Matrix accounts a verification may be asked for from, as the TUI picks
 * (domain.PickAccount): the only one, else the default, else every one to choose from.
 */
export function verifyAs(accounts: readonly Account[]): Account[] {
  const matrix = accounts.filter((a) => a.network === "Matrix");
  const picked = matrix.length === 1 ? matrix[0] : matrix.find((a) => a.default);
  return picked === undefined ? matrix : [picked];
}
