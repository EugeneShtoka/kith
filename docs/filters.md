# Filters and Spam

A **filter** catches messages by their words or their sender and moves the room they
arrived in into a **tag**. The starter config's **Spam** tag is where unwanted
conversations go, but nothing about it is built in: it is an ordinary tag whose
properties make it quiet, and any tag can have filters moving rooms into it.

## The Spam tag

```toml
[[tag]]
name            = "Spam"
counts_unread   = false                         # out of the unread totals
notifies        = false                         # nothing in it notifies
exclusive       = ["tags", "spaces", "origin"]  # out of every other tag and space
hide_when_empty = true

[[keys.tag]]
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
| Room list, timeline | `!` (a [`[[keys.tag]]`](configuration.md) binding) | The room goes into Spam, or comes out |
| Room list | `S` | The filing picker: tick or untick any tag |
| Composer | `/tag Spam` | The same, for the room you are writing in |

Putting a room in writes it into the tag's `picked`; taking it out removes it, or, for
a room the tag's rule or a filter put there, writes it into `excluded`. `excluded`
outranks `picked`, the rule and every filter.

Taking out a room a **filter** moved also writes a **release** to the room's account
data, so no filter, on this machine or any other running kith with your account,
moves it back. If the account data cannot be written, the status line says so; the
`excluded` entry still holds it out here.

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
from  = "@whatsapp_15550100:example.org"   # everything this sender writes

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
| `moves` | Which of the rules below its catch moves a room by: `always`, `first_message`, `direct`, `ratio`. Default `["first_message", "direct"]` |
| `ratio`, `floor`, `window` | The `ratio` move's numbers: the share caught (default 0.6), the least message count (5) and the period (`"168h"`) |

A filter needs `words`, `from` or both, and a tag that exists; one without is refused
at startup. Two filters may catch one message: each moves the room into its own tag.
Renaming a tag renames the filters and keys naming it; deleting it deletes them.

Adding a filter from **Settings → Filters → Add one** asks for each part in turn and
writes the filter only at the end.

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

### Preview before you trust a filter

`/caught` in the composer (or `:caught` on the command line) lists the messages your
filters' words catch, searched in the local message index. It ignores `in`, so it can
list messages in places a filter does not watch. From a room it starts with that room
and `tab` widens the scope; from the rail it starts with everything. Filters that
match by sender alone are counted in the summary but not listed.

### Rescanning what is already cached

Filters run as messages arrive, so a filter you add today does not look at yesterday.
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

## Why is this room here?

In a moved room, `W` (or `/why`) starts with each move: the tag, the filter and the
rule, such as "in Spam — the first message in it was caught by a filter (crypto)". If
a tag holding the room does not notify, it says so, then shows the
[notification rules](notifications.md#why-was-this-silenced) that would apply once
the room leaves it.

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

A room of kith's own WhatsApp, Telegram or Slack connection has no account data, so
its moves are kept in the local cache only. A tag's `picked` and `excluded` are part of
`config.toml`, stored on each machine with the rest of your configuration.

A room a muted tag holds is left out of notifications in the daemon before any
notification rule is consulted. [Verification-code
auto-copy](notifications.md#verification-codes-to-the-clipboard) still works there,
because copying a code you asked for does not interrupt you.
