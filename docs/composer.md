# Writing messages

The composer is where you write. It is a multi-line editor with undo, Markdown, mentions, emoji, attachments, spell-checking and word completion. Each room has its own draft, and that draft survives restarts. A message that fails to send is kept and retried by the daemon.

Every key on this page is a default. The [`[keys.*]` tables](keybindings.md) let you rebind them, and `?` inside the client lists what is currently bound.

## Sending, new lines and the editing keys

Press `i` on the timeline to start writing. By default **enter adds a new line** and **`ctrl+enter` or `alt+enter` sends**, so a message can run to as many lines as it needs.

| Key | What it does |
| --- | --- |
| `ctrl+enter`, `alt+enter` | Send |
| `enter` | New line |
| `esc` | Leave the composer (the draft stays) |
| `up` / `down` | Move between lines of the draft |
| `ctrl+a` / `home`, `end` | Start / end of the text |
| `ctrl+left` / `alt+b`, `ctrl+right` / `alt+f` | One word left / right |
| `ctrl+w`, `alt+backspace` | Delete the word before the caret |
| `ctrl+delete`, `alt+d` | Delete the word after the caret |
| `ctrl+u` / `ctrl+k` | Delete everything before / after the caret |
| `ctrl+z` / `ctrl+r` | Undo / redo |
| `ctrl+x` | Cut the whole draft to the clipboard |
| `alt+e` | Finish the message in `$EDITOR` |
| `alt+u` | Attach a file |
| `alt+s` | Walk through the misspellings |
| `ctrl+e` | Open the emoji browser |

`ctrl+enter` is only distinct from `enter` on terminals that speak the Kitty keyboard protocol or modifyOtherKeys (wezterm, kitty, foot, ghostty, recent xterm). `alt+enter` works everywhere. If you prefer enter to send, swap the pair:

```toml
[keys.insert]
send    = "enter"
newline = "ctrl+enter,alt+enter"
```

The settings screen (`,`) has an "Enter key" row that changes both at once.

### Undo, redo and cut

`ctrl+z` and `ctrl+r` work in every text field: the composer, prompts and filters. They step through whole edits rather than one character at a time, so the keystroke that deleted a sentence takes one press to undo. `ctrl+x` moves the whole draft to the clipboard and empties the field, and `ctrl+z` brings it back. Each autocorrection and each accepted spelling suggestion is its own undo step.

## Markdown

With `[composer] markdown = true` (the default), what you type is rendered to the HTML body Matrix carries alongside the plain one. The dialect is CommonMark, the same one Element's composer uses.

| You type | Recipients see |
| --- | --- |
| `**bold**` or `__bold__` | **bold** |
| `_italic_` or `*italic*` | *italic* |
| `` `code` `` | inline code |
| `~~struck~~` | struck-through text |
| `[text](https://example.org)` | a named link |
| `> quoted` | a block quote |
| `- item` / `1. item` | a list |
| ```` ``` ```` fenced block | a code block |
| `\|\|hidden\|\|` | a spoiler |

Some things are deliberately left out:

- **Tables are not rendered.** No chat network displays them, and bridges would collapse the cells into a run of words. The plain body keeps whatever columns you lined up by hand.
- **A single `~` does not strike through.** That keeps `~/path` and "~5 minutes" intact.
- **HTML you type is escaped.** Typing `<b>` sends those three characters.
- `*x*` is italic, as CommonMark says, and not bold as in some chat apps.

An ordinary sentence with no formatting is sent as plain text with no HTML body. The plain body is always exactly what you typed.

**Why it matters on bridged accounts:** bridges convert the HTML body, not the characters. Without it, your asterisks reach WhatsApp, Slack or Telegram as literal asterisks.

To send one message exactly as typed, start it with `/plain`. To turn rendering off for good:

```toml
[composer]
markdown = false
```

## Mentions and room links

Type `@` at the start of the message or after a space to open the member list. It narrows as you type:

| Key | In the popup |
| --- | --- |
| `ctrl+n`, `down` | Next candidate |
| `ctrl+p`, `up` | Previous candidate |
| `tab`, `enter` | Accept |
| `esc` | Close, keeping what you typed |

Accepting inserts the person's display name and records a real Matrix mention. When the message is sent, the name becomes a `matrix.to` pill that notifies them. A name you type out by hand is just text and notifies nobody.

The pill is tied to the name in the text. If you delete or change the name before sending, including in `$EDITOR`, that person is no longer mentioned. Only the first time a name appears is turned into a pill.

`#` works the same way for rooms: it offers every room you are in and links the one you pick.

## Replies, threads and edits

| Key (timeline) | What it does |
| --- | --- |
| `r` | Reply to the selected message |
| `E` | Edit one of your own messages |
| `t` | Open the thread the message is in, and write there |
| `T` | Start a thread on the selected message |

- **Replying:** the composer's gutter shows `↪` and the name of the person you are answering.
- **Editing:** `E` puts the message's text in the composer, and the gutter shows `✎ editing`. Sending replaces what everyone sees. `esc` puts back whatever you were writing before. An edit that empties the message is refused; to delete a message, use `x` on the timeline. Matrix does not let you edit anyone else's messages.
- **Threads:** while a thread is open, the gutter says `thread`, and what you send goes to that thread.

## Writing in `$EDITOR`

Press `alt+e` in the composer, or on the timeline, to hand the draft to your editor. When the editor exits, its contents replace the draft. **Nothing is sent** until you press send. If the editor exits with an error, the draft is left as it was.

```toml
[composer]
editor      = ""     # empty: $VISUAL, then $EDITOR, then vi
file_suffix = ".md"  # tells the editor what kind of file it is editing
```

`editor` is split on spaces into a command and its arguments, and no shell is involved. So `"code --wait"` works, but quoting such as `"nvim -c 'set ft=md'"` does not.

## Emoji

There are three ways to insert an emoji:

1. **Shortcodes.** Type `:` at the start or after a space, and a popup completes the shortcode. If you type the whole shortcode, such as `:tada:`, it is replaced by the emoji directly.
2. **The browser.** `ctrl+e` opens a grid of every emoji. Type to filter it, use the arrows to move, and press `enter` or `tab` to insert. From the timeline, the same key reacts to the selected message instead.
3. **Reactions.** `e` on the timeline opens the reaction prompt, where the ten quick slots are bound to `1`–`9` and `0`.

**All three share one ranking.** Each emoji is scored by how often you use it: 10 points for each use in this room, 3 for each use in the room's spaces, and 1 for each use anywhere else. Emoji you have never used keep their alphabetical place. The ranking is fetched when you open a room and then stays fixed for the session, so nothing moves while you are looking at it. Emoji you type in messages and emoji you react with are ranked separately.

```toml
[display]
skin_tone = "none"      # none, light, medium-light, medium, medium-dark, dark

[display.emoji]
set       = "curated"   # curated (395), standard (1366) or complete (1872)
extra     = { shrug = "🤷", table_flip = "(╯°□°）╯︵ ┻━┻" }

[display.reactions]
scope  = "room"         # room, space or global
static = ["👍", "❤️", "😂", "🎉", "😮", "😢", "🙏", "👀", "🔥", "💯"]
```

`extra` shortcodes take precedence over the built-in names, so you can use it to redefine one.

## Attachments

Press `alt+u` while writing. kith asks your desktop for a file over the XDG file-chooser portal, the same interface Firefox uses. You get whichever chooser your desktop is set up with: a GTK or KDE dialog, or a terminal file manager. **What you have already typed becomes the file's caption.**

If no portal answers, for example over SSH or on a headless machine, kith prompts for a path instead.

You can also type the command, which is handy when the path is already on your clipboard:

```text
/upload ~/Pictures/whiteboard.png
/upload ~/report.pdf | Q3 numbers, page 4 is the interesting one
```

Anything after the `|` is the caption. A leading `~` is expanded, and nothing else is.

To save attachments other people sent, use `s` and `S` on the timeline. The [usage guide](usage.md) covers both.

## Drafts

**A draft belongs to its room.** If you switch rooms, the composer empties. When you come back, everything is where you left it: your words, the caret, mention pills, the reply target, an edit in progress, and the undo history.

- The daemon stores drafts, so they survive quitting the client and restarting the machine. The undo history is kept only for the current session.
- A draft is saved a moment after you stop typing, at least every five seconds while you keep typing, when you leave the room, and when you quit (which waits up to two seconds for the daemon). Every other way out (the interrupt key, SIGTERM, SIGHUP when the terminal closes, another window taking over) writes once more when the screen is gone.
- If the daemon does not take that last write (it is down), kith says so as it exits, and the words typed since the last save are gone: at most the last few seconds.
- A draft remembers the thread it is written in. Opening a room whose draft is in a thread opens that thread, and moving between threads with words in the composer takes them along, as sending would.
- One kith window at a time is open on an account (see [`--force`](cli.md)), so the drafts have one writer that types. [kith-mcp](mcp.md) only ever adds to a draft: its words go after yours, and a draft sent or cleared while it was writing does not come back. A draft you only visit is not written, so it keeps who drafted it.
- Rooms that hold a draft are marked `✎` after their name in the room list.
- The rail has a **Drafts** group that collects them.
- `s d` in the room list moves rooms with drafts to the top.
- A draft restored from disk shows who wrote it and how long ago, for example `✎ drafted 2h ago` or `✎ drafted by claude-code 5m ago`, until you type in it. Drafts written by an assistant through [kith-mcp](mcp.md) appear this way.

## The send queue

If the homeserver does not accept a message, for example because the network dropped, a token is being refreshed, or the server is restarting, **the daemon keeps it and retries**. The status line says `send failed — queued, retrying`. The retry reuses the original transaction ID, so a send that actually got through before the connection died does not arrive twice. Because the daemon runs on its own, the message goes out when the connection comes back, even if the terminal has been closed.

Edits are not queued, because a replacement that arrives late is worse than one that failed. If there is no daemon to hand the message to, the words go back into that room's draft rather than being lost.

## Scheduled messages

| Command | Example | Meaning |
| --- | --- | --- |
| `/at <time> <message>` | `/at 09:00 good morning` | Send at a clock time; a time already past today means tomorrow |
| `/in <delay> <message>` | `/in 2h see you then` | Send after a delay |
| `/scheduled` | | What is waiting to go out in this room; `enter` cancels one |
| `:scheduled` | | The same list across every room |

`/at` accepts `15:04`, `1504`, `3pm` and `3:04pm`. `/in` accepts Go durations: `30m`, `2h`, `1h30m`.

A scheduled message keeps its reply target, its mentions and the thread it was written in. The daemon owns the queue, so it sends on time whether or not the client is open. The queue is a TOML file you can read and edit: `$XDG_STATE_HOME/kith/scheduled-<account>.toml`, which is under `~/.local/state` by default. Failed sends from the send queue go into the same file.

If the machine was off past a message's send time, the message still goes out. A message more than `overdue_cutoff_hours` late (24 by default) is held instead, and `/scheduled` shows it until you decide what to do with it:

```toml
[schedule]
overdue_cutoff_hours = 24   # 0 = never too late
```

## Spell-checking

kith checks what you write as you type, using any engine that speaks the ispell pipe protocol: hunspell (the default), enchant, nuspell or aspell. Misspelled words get a curly underline.

```toml
[spell]
enabled           = true
command           = "hunspell"
dictionaries      = []        # empty: every installed dictionary
underline         = "curly"   # curly, line or none
check_before_send = false
autocorrect       = "off"     # off, misspellings, rare or all
```

**Leave `dictionaries` empty.** kith then loads every installed dictionary into one hunspell process, and a word is accepted if any of them accepts it. That means a message mixing English and Hebrew is checked correctly without declaring any languages.

### Installing dictionaries

Dictionaries are separate packages. When there is nothing to check with, kith says so once and prints the install command for your distribution. You can also install dictionaries into kith's own directory, with no root needed:

```sh
kith --add-dictionary list    # what can be installed, with sizes
kith --add-dictionary he_IL   # fetch one, checked against a pinned SHA-256
```

kith also looks at the languages in your own cached messages and offers the matching dictionaries once. If you untick a row and accept, it is written to `[spell] declined` and never offered again. Pressing `esc` only dismisses the offer for now.

### The correction walk

`alt+s` in the composer, or `z=` from the timeline, walks through the misspellings one at a time, with suggestions for each:

| Key | What it does |
| --- | --- |
| `1`–`9` | Take that suggestion and move to the next word |
| `s`, `space` | Skip this word |
| `a` | Add it to your personal dictionary, permanently |
| `i` | Accept it for this session only |
| `esc` | Stop, leaving the rest underlined |

Words you add go into `personal.dic` in kith's dictionary directory, which is `~/.local/share/kith/hunspell` by default.

With `check_before_send = true`, sending a message that contains misspellings opens the walk first, and the message sends once you are through it. `esc` stops both the walk and the send, so nothing is ever sent without you.

`autocorrect` fixes a word as you finish it, but only when the fix is unambiguous: the engine offers exactly one suggestion, or its first suggestion is one edit away and that edit is a case change, a transposition or a missing letter. "teh" becomes "the". A name or anything with several plausible readings stays underlined for you to decide.

### Rare-word detection

Some languages' dictionaries accept almost any string. Hebrew's accepts 23% of random three-letter strings, so a typo that happens to form a valid word goes unflagged. Rare-word detection asks a second question: how often do people actually write this word? If a much more common word is one slip away, kith marks the word.

This needs word-frequency lists, which are a separate and larger download:

```sh
kith --add-frequencies list
kith --add-frequencies he_IL
```

```toml
[spell]
flag_rare_words  = true
rare_ratio       = 100        # how many times commoner the neighbour must be
rare_underline   = "dotted"   # dotted, line or none
rare_before_send = false      # also stop the check-before-send walk on these
```

A rare word is a hint, not an error. It is marked more quietly than a misspelling, and it never holds up a send unless you set `rare_before_send`. It is autocorrected only when `autocorrect = "rare"` or `"all"`. A language that shares its script with another installed dictionary is skipped, because a word alone does not say which of the two languages it is in.

## Word and phrase completion

kith completes words from what has recently been said in the room, the room's spaces and your account as a whole: about the last 1000 messages of this conversation, 3000 of its spaces, 2000 of your own and 5000 overall. So jargon from last month outranks words from two years ago, and a completion costs the same however much history is cached. Where recent history runs out, the installed word-frequency list fills in. **Nothing is inserted until you ask.**

- Suggestions start after three characters of a word, and only when they would save you at least two more.
- **Ghost text:** when one candidate clearly leads, it is drawn dimmed after the caret.
- **Phrases:** after you finish a word, kith can suggest what usually follows your last two words in this room, based on the room's own history.

| Key | What it does |
| --- | --- |
| `right` | Take one word of the suggestion |
| `tab` | Take the whole suggestion, or open the chooser when nothing is confident enough to draw |
| `alt+1` … `alt+5` | Take that candidate directly |

In a right-to-left word, the arrows are swapped, so the key that points *into* the suggestion is the left arrow. This follows the word being typed, not the whole message.

```toml
[complete]
enabled = true
ghost   = true
ratio   = 3                          # how far the leader must outweigh the runner-up
scope   = "room"                     # room, space or global
sources = ["history", "frequency"]   # empty means both
```

`scope` weights where a word was used; it does not filter anything. At `"room"`, a use in this room counts 10, a use in its spaces counts 3, and a word you wrote yourself gets 5 more wherever it was. At `"space"`, the room weight goes away. At `"global"`, every use counts once. Raising `ratio` makes the ghost appear less often and be right more often.

An optional local language model can add next-word suggestions to the same keys. It runs entirely on your machine. See [Assist](assist.md).

## Commands

A `/` at the very start of the composer opens the command list: `/me`, `/plain`, `/upload`, `/at`, `/in`, `/summary`, the `/scheduled` family, and any scripts you have written. To send a message that starts with a slash, begin it with `//`. See [Commands](commands.md).

## Related

- [Commands](commands.md): the full list, and writing your own
- [Assist](assist.md): the local completion model and optional remote summaries
- [Keybindings](keybindings.md): rebinding everything above
- [Configuration](configuration.md): where the config file lives and how it reloads
