// The composer's `/` commands, as the TUI's (internal/tui/slash.go): the same table, read
// the same way. A known command is not a message; any other slash word is a command of
// the device's commands directory if it holds one (kith-relay runs it), else a message.
// `//` escapes one slash, and words over more than one line are never a command. The
// commands whose action the window has not got say so, rather than half-working.
import type { Asked } from "./polls";

/** One of the TUI's commands, as the window offers it. */
export interface Builtin {
  /** With its slash: "/me". */
  name: string;
  /** Names the argument for the usage line; "" takes none. */
  arg: string;
  /** A command with a required argument refuses to run without one. */
  argOptional: boolean;
  summary: string;
  /** The window does what it does; false: only the TUI does, for now. */
  window: boolean;
}

const tuiOnly = (name: string, summary: string, arg = "", argOptional = arg !== ""): Builtin => ({
  name,
  arg,
  argOptional,
  summary,
  window: false,
});

/** builtins is the TUI's table, in its order (internal/tui/testdata/commands.json holds the names to it). */
export const builtins: readonly Builtin[] = [
  {
    name: "/me",
    arg: "<what you are doing>",
    argOptional: false,
    summary: "send as an emote — “* you wave” rather than “you: waves”",
    window: true,
  },
  tuiOnly("/summary", "what this room has been saying, by the completion model", "[2h | 7d | yesterday | 50]"),
  tuiOnly("/export", "write this room to a Markdown file", "[span] [~/folder]"),
  {
    name: "/plain",
    arg: "<message>",
    argOptional: false,
    summary: "send it exactly as typed — no Markdown",
    window: true,
  },
  {
    name: "/upload",
    arg: "",
    argOptional: false,
    summary: "send a file — the window picks it in the open dialog",
    window: true,
  },
  {
    name: "/at",
    arg: "<time> <message>",
    argOptional: false,
    summary: "send it later — /at 09:00 good morning",
    window: true,
  },
  {
    name: "/in",
    arg: "<delay> <message>",
    argOptional: false,
    summary: "send it after a delay — /in 2h see you then",
    window: true,
  },
  tuiOnly("/tracked", "where your tracked words came up here", "[word]"),
  tuiOnly("/files", "every message with a file in this room"),
  tuiOnly("/mentions", "where you were named in this room"),
  { name: "/search", arg: "[terms]", argOptional: true, summary: "search this room", window: true },
  tuiOnly("/hidden", "show the messages of people you hid"),
  tuiOnly("/threads", "the conversations in this room"),
  tuiOnly("/starred", "what you have starred here"),
  { name: "/scheduled", arg: "", argOptional: false, summary: "what is waiting to go out in this room", window: true },
  tuiOnly("/ignored", "the Matrix users you ignore"),
  tuiOnly("/discover", "the Matrix public rooms directory"),
  tuiOnly("/knock", "ask to be let into a knock-only Matrix room", "<room> [reason]"),
  {
    name: "/poll",
    arg: "<question> | <answer> | <answer> [| …] [| any]",
    argOptional: false,
    summary: "ask a poll in this room — trailing “| any” lets voters pick several",
    window: true,
  },
  { name: "/topic", arg: "", argOptional: false, summary: "what this room is for, in full", window: true },
  tuiOnly("/why", "why this room does or does not notify you"),
  tuiOnly("/unread", "mark this room unread"),
  tuiOnly("/tag", "put this room in a tag, or take it out", "[name]"),
  tuiOnly("/caught", "what the filters catch here"),
  tuiOnly("/direction", "read this room right to left, then left to right"),
  tuiOnly("/shortcut", "a key sequence that goes to this room", "[keys]"),
  { name: "/replacement", arg: "", argOptional: false, summary: "go to the room that replaced this one", window: true },
  tuiOnly("/invite", "invite somebody to this room", "<@user:server>", false),
  tuiOnly("/unban", "lift a ban in this room", "[@user:server]"),
  tuiOnly("/leave", "leave this room"),
];

/** What words sent read as. */
export type Typed =
  /** A message: body is what goes out (one slash of `//` taken off). */
  | { kind: "message"; body: string }
  /** One of the TUI's commands, its argument after it. */
  | { kind: "builtin"; command: Builtin; arg: string }
  /** Any other slash word: a command of the commands directory, if it holds one. */
  | { kind: "user"; name: string; arg: string };

/** argOf is the TUI's commandArg: space or colon separates; the bare command has none. */
function argOf(body: string, name: string): string | undefined {
  if (body === name) {
    return "";
  }
  for (const sep of [" ", ":"]) {
    if (body.startsWith(name + sep)) {
      return body.slice(name.length + 1).trim();
    }
  }
  return undefined;
}

/** unescaped is a message's words as they go out: `//` loses one slash (Element's convention). */
export function unescaped(body: string): string {
  return body.startsWith("//") ? body.slice(1) : body;
}

/** firstLine is text's first line, as a status says a command's output. */
export function firstLine(text: string): string {
  return text.split("\n", 1)[0] ?? "";
}

/** typed reads words as the TUI's submit does: a command, or a message. */
export function typed(words: string): Typed {
  const body = words.trim();
  if (body.startsWith("//")) {
    return { kind: "message", body: unescaped(body) };
  }
  if (!body.startsWith("/") || body.length === 1 || body.includes("\n")) {
    return { kind: "message", body };
  }
  for (const command of builtins) {
    const arg = argOf(body, command.name);
    if (arg !== undefined) {
      return { kind: "builtin", command, arg };
    }
  }
  const space = body.indexOf(" ");
  return space < 0
    ? { kind: "user", name: body.slice(1), arg: "" }
    : { kind: "user", name: body.slice(1, space), arg: body.slice(space + 1).trim() };
}

/** A command of the commands directory, as the relay lists it for the open room. */
export interface UserCommand {
  name: string;
  applies: boolean;
  places: readonly string[];
}

/** A row of the `/` list: what it shows, and what taking it puts in the composer. */
export interface Offered {
  name: string;
  label: string;
  detail: string;
  /** Put in the composer: the name, and a space where an argument follows. */
  insert: string;
}

/**
 * offered is the `/` list for words being typed, the caret at caret: only while they
 * are one slash word at the very start. The TUI's built-ins first, then the commands of
 * the directory that run here (a built-in wins a name), each matching what is typed.
 */
export function offered(words: string, caret: number, users: readonly UserCommand[]): Offered[] | undefined {
  const head = words.slice(0, caret);
  if (!head.startsWith("/") || head.startsWith("//") || /\s/.test(head)) {
    return undefined;
  }
  const q = head.slice(1).toLowerCase();
  const rows: Offered[] = [];
  for (const c of builtins) {
    if (c.name.slice(1).startsWith(q)) {
      rows.push({
        name: c.name,
        label: c.arg === "" ? c.name : `${c.name} ${c.arg}`,
        detail: c.window ? c.summary : `${c.summary} — in the TUI; not in the window yet`,
        insert: c.arg === "" ? c.name : `${c.name} `,
      });
    }
  }
  for (const u of users) {
    const name = `/${u.name}`;
    if (u.applies && u.name.toLowerCase().startsWith(q) && !builtins.some((c) => c.name === name)) {
      rows.push({ name, label: name, detail: `yours — ${u.name} in your commands folder`, insert: name });
    }
  }
  return rows;
}

/** pollOf is the TUI's parsePollArg: question | answers… [| any], or undefined. */
export function pollOf(arg: string): Asked | undefined {
  const parts = arg.split("|").map((p) => p.trim());
  if (parts.length < 3) {
    return undefined;
  }
  const [question = "", ...answers] = parts;
  let multiple = false;
  if (answers.at(-1)?.toLowerCase() === "any") {
    multiple = true;
    answers.pop();
  }
  if (question === "" || answers.length < 2 || answers.includes("")) {
    return undefined;
  }
  return { question, options: answers, multiple };
}
