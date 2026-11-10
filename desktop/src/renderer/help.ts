// The help overlay's rows (`?`, as the TUI's view_help.go draws its own): each key the
// window binds, under where it acts, with the keys [keys] gives it now. The device's
// [[keys.jump]] are drawn after them.
import type { Keys } from "./keymap";

/** A help row: the [keys] actions it is about, and what they do. */
export interface HelpRow {
  paths: readonly string[];
  what: string;
}

/** A help section: where its keys act, and its rows. */
export interface HelpSection {
  title: string;
  rows: readonly HelpRow[];
}

const row = (what: string, ...paths: string[]): HelpRow => ({ paths, what });
const numbered = (prefix: string, n: number) => Array.from({ length: n }, (_, i) => `${prefix}${String(i + 1)}`);

/** helpSections are the window's keys, in the order its docs list them (docs/desktop.md). */
export const helpSections: readonly HelpSection[] = [
  {
    title: "Anywhere",
    rows: [
      row("Go to a room, person or space, while typing too", "jump_to"),
      row("Back to the room you were in before, while typing too", "jump_back"),
      row("Forward again, while typing too", "jump_forward"),
      row("The next pane, or the one before", "focus_next", "focus_prev"),
      row(
        "Straight to places, rooms, the open room or its composer, while typing too",
        "focus_rail",
        "focus_rooms",
        "focus_timeline",
        "focus_composer",
      ),
      row("These keys", "help"),
      row("Settings", "settings"),
    ],
  },
  {
    title: "While a voice note or video plays (ahead of the rest)",
    rows: [
      row("Play or pause", "player.play_pause"),
      row("Back or forward by the skip step", "player.back", "player.forward"),
      row("Slower or faster", "player.slower", "player.faster"),
      row("Back to the speed set", "player.normal_speed"),
      row("Stop", "player.stop"),
    ],
  },
  {
    title: "Places, rooms and messages",
    rows: [
      row("The cursor up or down; a count before it moves that many", "nav.up", "nav.down"),
      row("Into the next pane: a place's rooms, a room's messages", "nav.open"),
      row("Back a pane; from a message, the open thread closes first", "nav.back"),
      row("A screen up or down", "nav.page_up", "nav.page_down"),
      row("Half a screen up or down", "nav.half_page_up", "nav.half_page_down"),
      row("The first row, or the room's first message", "nav.select_oldest", "nav.scroll_oldest"),
      row("The last row, or the newest message", "nav.select_newest", "nav.scroll_newest"),
      row("Mark a place's unread rooms read; it asks first", "rail.mark_read"),
    ],
  },
  {
    title: "On a room",
    rows: [
      row("File it into tags and spaces", "rooms.spaces"),
      row("Name it, for you alone", "rooms.name"),
      row("The phonebook card of the person in a direct chat", "rooms.person"),
      row("Mark it read without opening it; on a thread's row, that thread", "rooms.mark_read"),
      row("Mark it and all its threads read", "rooms.mark_all_read"),
      row("Mark it unread, or clear the mark", "rooms.mark_unread"),
      row("Hide its threads, or show them again; from a thread's row too", "rooms.fold_threads"),
    ],
  },
  {
    title: "On a message",
    rows: [
      row("Write a message", "timeline.insert"),
      row("Reply to it", "timeline.reply"),
      row("Edit it, if it is yours", "timeline.edit"),
      row("Delete it, if it is yours", "timeline.redact"),
      row("React", "timeline.react"),
      row("Go to the message it answers", "timeline.go_reply"),
      row("Open the thread under it, or start one on it", "timeline.open_thread", "timeline.start_thread"),
      row("The next or previous message that names you", "timeline.next_mention", "timeline.prev_mention"),
      row("The next or previous message with a file", "timeline.next_attachment", "timeline.prev_attachment"),
      row("The next day, or back to this day's start", "timeline.next_day", "timeline.prev_day"),
      row("The sender's phonebook card", "timeline.person"),
      row("Name the sender: the same person as others, their color and picture", "timeline.name"),
      row("Save its attachment", "timeline.download"),
      row("Play its voice note or video, or pause it", "timeline.play"),
      row("Its poll's options", "timeline.vote"),
      row("Copy its text, or a link from it", "timeline.copy_text", "timeline.copy_url"),
      row("Its history: every version, and its deletion and attachment", "timeline.history"),
      row("Open a link from it, staying here or going to the browser", "timeline.open_url", "timeline.open_url_focus"),
    ],
  },
  {
    title: "In the composer",
    rows: [
      row("Send", "insert.send"),
      row("A new line", "insert.newline"),
      row("Back to the message cursor, dropping a reply or a correction", "insert.cancel"),
      row("Take a word of the completion, or all of it", "insert.complete", "insert.complete_all"),
      row("Take one of the next words offered", ...numbered("insert.choose_", 5)),
      row("Attach a file", "insert.attach"),
      row("The emoji picker", "emoji.open"),
      row("The correction walk", "spell.open_insert"),
    ],
  },
  {
    title: "A list of names, emoji or commands",
    rows: [
      row("The next or previous one", "completion.next", "completion.prev"),
      row("Take it", "completion.accept"),
      row("Close the list", "completion.dismiss"),
    ],
  },
  {
    title: "The correction walk",
    rows: [
      row("Take a suggestion", ...numbered("spell.choose_", 9)),
      row("Add it to your dictionary", "spell.add"),
      row("Ignore it for this session", "spell.ignore"),
      row("Skip it", "spell.skip"),
      row("Ignore all, or skip all", "spell.ignore_all", "spell.skip_all"),
      row("Stop", "spell.stop"),
    ],
  },
  {
    title: "Reactions and the emoji picker",
    rows: [
      row("Take the first emoji found", "emoji.accept"),
      row("Close the picker", "emoji.cancel"),
      row("Close the quick reactions", "react.cancel"),
    ],
  },
];

/** keysOf is a row's keys as [keys] binds them now, or "(unbound)". */
export function keysOf(r: HelpRow, keys: Keys): string {
  return r.paths.flatMap((p) => keys[p] ?? []).join(", ") || "(unbound)";
}

/**
 * filtered is the sections a query leaves, case ignored, as the TUI's filterHelp does: a
 * section whose title matches stays whole, otherwise it keeps the rows whose keys or
 * words match, or goes.
 */
export function filtered(sections: readonly HelpSection[], keys: Keys, query: string): HelpSection[] {
  const q = query.trim().toLowerCase();
  const matches = (s: string) => s.toLowerCase().includes(q);
  return sections.flatMap((s) => {
    const rows = matches(s.title) ? s.rows : s.rows.filter((r) => matches(r.what) || matches(keysOf(r, keys)));
    return rows.length > 0 ? [{ ...s, rows }] : [];
  });
}
