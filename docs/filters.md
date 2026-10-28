# Filters and Spam

A **filter** catches messages by their words, their sender or their language and moves
the room they arrived in into a **tag**. The starter config's **Spam** tag is where unwanted
conversations go, but nothing about it is built in: it is an ordinary tag whose
properties make it quiet, and any tag can have filters moving rooms into it.

## The Spam tag

```toml
[[tag]]                                         # config.toml
name            = "Spam"
counts_unread   = false                         # out of the unread totals
notifies        = false                         # nothing in it notifies
exclusive       = ["tags", "spaces", "origin"]  # out of every other tag and space
hide_when_empty = true
```

```toml
[[keys.tag]]       # device.toml
chord = "!"        # the mail clients' "report spam" key
tag   = "Spam"
```

A room in Spam stays open, readable, searchable and answerable. kith never leaves the
Matrix room, and nobody else in the room can tell. Change any of these to taste: drop
`"origin"` and a spam room stays in the space it belongs to (a bridge's space, its
home); drop `notifies = false` and it notifies again. The properties are listed under
[Tags](usage.md#tags).

## Putting a room in a tag by hand

| Where | Key / command | Effect |
| --- | --- | --- |
| Room list | `!` (a [`[[keys.tag]]`](configuration.md) binding) | The room goes into Spam, or comes out |
| Room list | `S` | The filing picker: tick or untick any tag |
| Composer | `/tag Spam` | The same, for the room you are writing in |

Putting a room in writes it into the tag's `picked`; taking it out removes it, or, for
a room the tag's rule or a filter put there, writes it into `excluded`. `excluded`
outranks `picked`, the rule and every filter.

Taking out a room a **filter** moved also writes a **release** to the room's account
data, so no filter, on this machine or any other running kith with your account,
moves it back. If the account data cannot be written, the status line says so; the
`excluded` entry still holds it out here.

## Hiding a person

In a large group, one spammer cannot move the room. You can mark the person instead.

| Where | Key / command | Effect |
| --- | --- | --- |
| Timeline | `!` on a message | Its sender goes into Spam, or comes out |
| Timeline | `z h`, or `/hidden` in the composer | Show the hidden messages in this room until you leave it, or hide them again |

In the timeline every [`[[keys.tag]]`](configuration.md) key acts on the selected
message's sender, not the room; file the room from the room list or with `/tag`. A
marked person's ID is written into the tag's `people` list. While the tag is muted
(`notifies = false`), the person is hidden in every room. That covers their messages
and the direct replies to them, but not replies to those replies. One ID covers every
ID kith links to the same person: an identity (`a`) and the people directory, even a
link learned after you marked them. A person in a tag that notifies is listed but not
hidden. Your own messages are never hidden. Nothing is deleted: take the mark off
and everything comes back.

To hide someone in one profile only, name them in the profile's `people.exclude`
([configuration.md](configuration.md#people-in-a-profile)).

A direct message with someone a muted tag holds moves into that tag, as the
[`direct` move](#the-moves) would move it: only a DM you never wrote in, never one you
took out of the tag or its `excluded` names. It moves when they are marked, and when
they write in it later. Like every move it stays when you take the person out: take
the room out too if you want it back. `/why` in it says "it is a direct message with
someone you put in that tag", or names the filter that marked them.

A filter can mark a person too, with the [sender moves](#marking-the-sender). The key
takes a filter's mark off as it takes yours: it writes a release, so no filter on any
of your machines marks them again, by any of their IDs.

### Who a tag holds

`gp` in a tag's room list switches it to the tag's **people**, and back; the title says
how many. There is one row per person, whatever IDs they were marked by, with the
networks they are on, who put them there (you, or the filter by name), when a filter
did, and how many messages of theirs are cached. On a row:

- the tag's key takes them out, as on their message: your entry goes and a filter's
  mark is released;
- `enter` or `I` opens their [phonebook](phonebook.md) card;
- `gI` shows [everything they said](search.md#everything-one-person-said), hidden or
  not.

In Settings → Places, a tag's page lists the people you put there (**People**,
editable like its picked rooms) and, read-only, those **put here by filters**.

Beyond the timeline, a hidden person and the direct replies to them:

- never notify, and never move a room by a [filter](#filters);
- are left out of search and of `from:` completion;
- are left out of what the assistant reads (`read_room`, `read_around`,
  `search_messages`). If kith-mcp cannot read the people directory, it refuses those
  reads rather than risk showing them;
- are left out of unread and mention counts, threads included, and of the
  assistant's `unread_summary`. Marking or unmarking someone recounts the rooms they
  wrote in. A room whose read position kith does not know shows the server's count,
  which still includes them;
- are left out of [`/summary` and `:todo`](assist.md#assist-tasks). `/summary 50` is
  the last fifty messages you can see.

## Filters

Filters catch **messages**. Nothing is caught until a `[[filter]]` exists.

```toml
[[filter]]
name  = "crypto"
tag   = "Spam"
words = ["*bitcoin*", "*binance*", "invest*"]

[[filter]]
name  = "that number"
tag   = "Spam"
from  = "matrix:@whatsapp_15550100:example.org"   # everything this sender writes

[[filter]]
name  = "elections"
tag   = "Spam"
words = ["vote*"]
in    = ["place:SMS"]   # only messages in the rail row shown as SMS
moves = ["always"]      # one caught message is enough
```

| Key | Meaning |
| --- | --- |
| `name` | Required. It is what `/why` names as having moved a room |
| `tag` | Required. The [[tag]] it moves rooms into |
| `words` | Words to match, with the same globs as tracked words: `pay` matches the whole word, `pay*` words that start with it, `*pay` words that end with it, `*pay*` anywhere. Case is ignored |
| `from` | Only messages from this sender. With no words (or only `*`), the filter catches everything they write |
| `in` | Only messages in these places, in the place vocabulary (`space:`, `tag:`, `place:`, `room:`, `protocol:`, `dm`, `group`). Empty is everywhere |
| `lang` | Only messages in one of these languages: codes (`"en"`, `"zh"`), a `family:` or a `region:`. See [Languages](#languages) |
| `not_lang` | Only messages that cannot be in any of these languages, in the same vocabulary as `lang` |
| `script` | Only messages written mostly in one of these writing systems: `Latin`, `Cyrillic`, `Hebrew`, `Arabic`, `Greek`, `Han`, `Kana`, `Hangul`, `Thai`, `Devanagari` and others |
| `moves` | Which of the rules below its catch moves a room by: `always`, `first_message`, `direct`, `ratio`; or marks its sender by: `sender_first`, `sender_ratio`, `sender_spread`. Default `["first_message", "direct"]` |
| `ratio`, `floor`, `window` | The `ratio` and `sender_ratio` moves' numbers: the share caught (default 0.6), the least message count (5) and the period (`"168h"`) |
| `spread` | How many rooms one caught message must reach for `sender_spread` (default 3, at least 2) |

A filter needs `words`, `from` or a language (`lang`, `not_lang`, `script`), and a tag
that exists; one without is refused at startup, as is a language or writing system it
does not know. With several, a message must pass each. Two filters may catch one message: each moves the room into its own tag.
Renaming a tag renames the filters and keys naming it; deleting it deletes them.

Adding a filter from **Settings → Filters → Add one** asks for each part in turn and
writes the filter only at the end. A place's page in **Settings → Places** lists its
filters and adds one with the place filled in: a tag's page one moving rooms into it,
a space's page one watching it.

### Languages

kith reads a message's language from its letters and does not guess it. The writing
system is certain from a single character: two Chinese characters are enough. Letters
only some languages use narrow it further: ы and э mean Russian or Belarusian, ї
Ukrainian, ß German, ơ and ư Vietnamese. What the letters cannot settle stays open, so
a message has **suspects**, the languages it could be in:

| Message | Suspects |
| --- | --- |
| `你好` | Chinese, then Japanese (Chinese characters with no kana) |
| `東京に行きます` | Japanese (any kana) |
| `안녕하세요` | Korean |
| `Вы где?` | Russian, Belarusian |
| `Да` | every language written in Cyrillic |
| `Ok, see you tomorrow` | every language written in Latin letters |
| `😀`, a bare link, `¯\_(ツ)_/¯` | none: no words |

Links, Matrix IDs, `:shortcodes:`, code and faces (kaomoji) are not read as words. A
telltale letter only counts when at least half of the message's words carry one, so
"I live in Zürich" stays English. A message written as much in a second writing
system as in its first has the suspects of both.

`not_lang` catches a message none of whose suspects is in the list. That makes it the
safe way to catch what you do not speak: `not_lang = ["en", "ru", "he"]` catches the
two-character Chinese message, but never `Ok` or `Да`, because English and Russian are
among their suspects. `lang` catches a message the letters settle into the list: either
every suspect is in it, or the writing system points to one language, as Chinese
characters point to Chinese. `lang = ["ru"]` does not catch `Вы где?`, which could be
Belarusian; `lang = ["family:slavic"]` does.

A short message in Latin letters with no telltale letters (Malay, Indonesian, Dutch)
cannot be told from English, so `lang` will not catch it and `not_lang` naming English
will not either.

Families and regions name several languages at once:

| Entry | Languages |
| --- | --- |
| `family:slavic` | Russian, Ukrainian, Belarusian, Bulgarian, Macedonian, Serbian, Croatian, Bosnian, Slovenian, Czech, Slovak, Polish |
| `family:germanic`, `family:romance`, `family:turkic`, `family:semitic`, `family:uralic`, `family:baltic`, `family:celtic`, `family:indo-iranian`, `family:dravidian`, `family:sino-tibetan`, `family:japonic`, `family:koreanic`, `family:kra-dai`, `family:austroasiatic`, `family:austronesian`, … | as the name says |
| `region:asia-east` | Chinese, Japanese, Korean, Mongolian |
| `region:asia-southeast` | Vietnamese, Thai, Lao, Khmer, Burmese, Indonesian, Malay, Tagalog |
| `region:asia-south`, `region:asia-central`, `region:asia-west`, `region:europe-east`, `region:europe-north`, `region:europe-south`, `region:europe-west`, `region:africa-north`, `region:africa-east` | the UN regions, each language placed by its main country |

Chinese and Japanese share no family; name a region, or both families, to catch them
together:

```toml
[[filter]]
name = "east asian"
tag  = "Spam"
lang = ["region:asia-east"]
```

### The moves

Filters judge messages. These rules decide when a caught message moves its **room**;
a filter lists the ones it uses in `moves`, tried in this order:

| Move | Fires when |
| --- | --- |
| `always` | The filter catches any message. For words that never belong in a place, such as campaign texts arriving over SMS. Pair it with `in`, or one stray word moves a room you care about |
| `first_message` | The first message in a room is caught. In an established room one caught message is noise; in a new room the first message is the whole relationship |
| `direct` | A message in a direct message is caught **and you have never written in that DM**. A DM is one person, so a verdict on the message is a verdict on the room. Never applies to groups, so one spammer among four hundred members cannot move a group |
| `ratio` | Within the filter's `window`, at least `floor` messages were sent and at least `ratio` of them (0–1) were caught by it. It can move a room that was fine yesterday, which is why no filter uses it unless it says so |

A room the tag's `excluded` names, or one released from that tag on any of your
machines, is never moved. A room whose history cannot be read from the cache is not
moved either, because the strongest moves rely on knowing that a room is new.

**Filters move; only you take out.** No filter takes a room out of a tag. A move
never changes back on its own, and a release is permanent on every machine: before a
move is written, the daemon checks the homeserver, and a room released elsewhere a
moment ago is left alone.

### Marking the sender

The room moves judge a room. In a group of four hundred, one spammer's messages are
a small share of it, so `direct` refuses and `ratio` rarely fires. The sender moves
judge the **person** who sent a caught message, over everything they wrote in every
room you can see, and put them in the filter's tag: in a muted one, such as Spam,
they are [hidden in every room](#hiding-a-person).

| Move | Marks the sender when |
| --- | --- |
| `sender_first` | The caught message is the first thing of theirs kith has seen anywhere |
| `sender_ratio` | Within `window`, they wrote at least `floor` messages, every room together, and at least `ratio` of them were caught by the filter |
| `sender_spread` | Within `window`, they sent the same caught message (the same words) in at least `spread` rooms. This is how a spam run looks in groups |

```toml
[[filter]]
name  = "spam runs"
tag   = "Spam"
words = ["*bitcoin*", "*airdrop*"]
moves = ["sender_spread", "direct"]   # mark the runner; a DM still moves
```

A filter may mix both kinds: each fires on its own. The same rails hold, and two
more:

- **Never anyone you have written to**: the other person in a direct chat you wrote
  in, and anyone whose message you replied to, under any ID kith links to them.
- **A release outranks every filter**: someone you took out of the tag is never
  marked again, by this ID or any ID linked to it.
- If kith cannot read their messages, whom you wrote to, or the people directory, it
  marks nobody.

In a muted tag, the message that marks them does not notify, and nothing of theirs
does after. A
Matrix ID's mark is kept in your account data, so it reaches your other machines;
another network's is kept by kith on this machine, as that network's room moves are.

### Preview before you trust a filter

`/caught` in the composer (or `:caught` on the command line) lists the messages your
filters' words catch, searched in the local message index. It ignores `in`, so it can
list messages in places a filter does not watch. From a room it starts with that room
and `tab` widens the scope; from the rail it starts with everything. It also ignores
language clauses, so it can list a message a filter's language would not catch.
Filters with no words (a sender or a language alone) are counted in the summary but
not listed.

### Rescanning what is already cached

Filters run as messages arrive, so a filter you add today does not look at yesterday.
Messages a network brings in later are judged as they reach the cache, like a rescan
of just those (the rules below): what a sync catches up after an outage or a lapsed
login, a page of scrollback, a rebuilt cache.

`:rescan` judges the messages already in the local cache, as if each had just arrived,
and lists the moves it would make, with the tag, the filter and the message:

```text
:rescan          everything cached
:rescan 30d      only messages from the last 30 days (also 12h, 1h30m)
```

Every move starts ticked. Untick the ones to leave alone with `space`, and `enter`
makes the rest; a room moves into every tag the rescan found for it. A rescan uses
`always`, `direct` and `ratio`, never `first_message`: the cache may not reach back
to a room's first message. It skips rooms already moved or released, the tag's
`excluded`, and your own messages.

The people the [sender moves](#marking-the-sender) would mark are listed beside the
rooms, ticked too, with the tag, the filter and the message that marks them. Each
sender is judged at each caught message by what they had written up to then, with
`sender_ratio` and `sender_spread`, never `sender_first`, for the same reason. People
already marked or released, anyone you have written to, and you are skipped. A
person seen under two linked IDs can be listed twice; marking either marks them.

## Why is this room here?

In a moved room, `W` (or `/why`) starts with each move: the tag, the filter and the
rule, such as "in Spam — the first message in it was caught by a filter (crypto)". If
a tag holding the room does not notify, it says so, then shows the
[notification rules](notifications.md#why-was-this-silenced) that would apply once
the room leaves it.

On a hidden message, shown with `z h`, `/why` starts with the person instead: whom
the message is from, or whom it answers, and what hid them, such as "in Spam — they
sent the same caught message in several rooms (spam runs)", or "in Spam — you put
them there".

## Where moves are stored

The daemon runs the filters as messages arrive, whether or not a terminal is open.
That matters most for `first_message`, which usually fires when nobody is looking.

A Matrix room's moves are written to its **account data** under the event type
`org.kith.tags`: per tag (lower-cased), the rule, the filter and a timestamp, or a
release. The local cache mirrors them. Account data lives on your homeserver, so:

- **It survives a cache rebuild.** Each move fires once, on a message that has gone
  by. A move kept only in the cache would be lost with it.
- **It follows the account.** Your other machines see the same moves and releases; a
  running daemon picks up a release made elsewhere within a minute. Other people in
  the room never see either.

A room of kith's own WhatsApp, Telegram, Slack or LinkedIn connection has no account
data, so its moves are kept in the local cache only. A tag's `picked` and `excluded` are part of
`config.toml`, stored on each machine with the rest of your configuration.

A room a muted tag holds is left out of notifications in the daemon before any
notification rule is consulted. [Verification-code
auto-copy](notifications.md#verification-codes-to-the-clipboard) still works there,
because copying a code you asked for does not interrupt you.
