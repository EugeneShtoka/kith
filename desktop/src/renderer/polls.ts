// A message's poll as the TUI draws and works it (tui polls.go, poll_create.go): the
// question, each answer with its count and whether it is this person's, a vote (several
// answers where the poll takes them) or the vote taken back, who chose what as far as
// the network names them, and a poll asked. New results arrive as the message's poll
// changing: nothing here counts votes.
import type { PollVotersResponse } from "../gen/backend/v1/services_pb";
import { type Poll, PollHiding, type PollOption, type PollVoter } from "../gen/backend/v1/types_pb";
import { compareBytes } from "./merge";

/** pollNote is what the question says of the poll: closed, or open to several answers. */
export function pollNote(p: Poll): string {
  return p.closed ? "closed" : p.multiple ? "choose any" : "";
}

/** share is how much of the vote an answer has, in whole percent; 0 with no votes (domain.Poll.Share). */
export function share(p: Poll, o: PollOption): number {
  const total = p.options.reduce((n, x) => n + x.votes, 0n);
  return total === 0n ? 0 : Number((o.votes * 100n) / total);
}

/**
 * pollTally is an answer's votes as words, "3 votes · 25%" (domain.Poll.Tally;
 * conformance table internal/domain/testdata/polls.json).
 */
export function pollTally(p: Poll, o: PollOption): string {
  return `${String(o.votes)} ${o.votes === 1n ? "vote" : "votes"} · ${String(share(p, o))}%`;
}

/** voted: this person has chosen an answer (domain.Poll.Voted). */
export function voted(p: Poll): boolean {
  return p.options.some((o) => o.mine);
}

/** canTakeBack: a vote of one answer may be taken back while the poll is open (the TUI's row for it). */
export function canTakeBack(p: Poll): boolean {
  return !p.multiple && !p.closed && voted(p);
}

/**
 * ticked is the answers ticked in a poll that takes several: this person's own until
 * they tick or untick one (touched), as the TUI's picker opens with them ticked.
 */
export function ticked(p: Poll, touched: ReadonlySet<string> | undefined): ReadonlySet<string> {
  return touched ?? new Set(p.options.filter((o) => o.mine).map((o) => o.id));
}

/** tick is ticked with id ticked, or unticked. */
export function tick(ticks: ReadonlySet<string>, id: string): ReadonlySet<string> {
  const next = new Set(ticks);
  if (!next.delete(id)) {
    next.add(id);
  }
  return next;
}

/** choice is the answers ticked, as a vote names them: in the poll's order; none takes the vote back. */
export function choice(p: Poll, ticks: ReadonlySet<string>): string[] {
  return p.options.filter((o) => ticks.has(o.id)).map((o) => o.id);
}

/** Who chose what in one poll, read as it is shown (PollVoters): one poll's at a time. */
export interface VotersRead {
  room: string;
  event: string;
  /** Answered: voters, or the error. */
  read: boolean;
  answer: PollVotersResponse | undefined;
  error: string;
}

/** you is how a poll names this person among its voters. */
export const you = "you";

/** voterName names a voter: "you", else as the network calls them. */
export function voterName(v: PollVoter, selves: ReadonlySet<string>): string {
  return selves.has(v.id) ? you : v.name || v.id;
}

/** chosenBy is who chose an answer, by name: "you" first, then sorted (tui chosenBy). */
export function chosenBy(voters: readonly PollVoter[], option: string, name: (v: PollVoter) => string): string[] {
  return voters
    .filter((v) => v.options.includes(option))
    .map(name)
    .sort((a, b) => (a === b ? 0 : a === you ? -1 : b === you ? 1 : compareBytes(a, b)));
}

/**
 * voterLines lists who chose each answer, an answer no one chose left out, with those
 * the network counts but does not name as "n more"; or why no one is named (tui
 * voterLines).
 */
export function voterLines(p: Poll, v: VotersRead, name: (v: PollVoter) => string): string[] {
  if (!v.read) {
    return ["Reading who voted…"];
  }
  if (v.error !== "") {
    return [`Could not read who voted: ${v.error}`];
  }
  switch (v.answer?.hidden) {
    case PollHiding.ANONYMOUS:
      return ["An anonymous poll: no one who voted is named"];
    case PollHiding.VOTE_FIRST:
      return ["Who voted is shown once you have voted"];
  }
  const voters = v.answer?.voters ?? [];
  const lines: string[] = [];
  for (const o of p.options) {
    const who = chosenBy(voters, o.id, name);
    if (who.length === 0 && o.votes === 0n) {
      continue;
    }
    const rest = o.votes - BigInt(who.length);
    if (rest > 0n) {
      who.push(`${String(rest)} more`);
    }
    lines.push(`${o.text}: ${who.join(", ")}`);
  }
  return lines.length > 0 ? lines : ["No one has voted yet"];
}

/** A poll to ask (CreatePoll). */
export interface Asked {
  question: string;
  options: string[];
  multiple: boolean;
}

/**
 * asked is the poll the form writes, or undefined while it asks none: a question and at
 * least two answers (tui parsePollArg). An answer left blank is a row not used.
 */
export function asked(question: string, answers: readonly string[], multiple: boolean): Asked | undefined {
  const q = question.trim();
  const options = answers.map((a) => a.trim()).filter((a) => a !== "");
  return q === "" || options.length < 2 ? undefined : { question: q, options, multiple };
}
