# Spam

Spam in kith is a place, not a mute. A room in Spam **leaves the group it was in**: it disappears from the rail's other groups, from every unread total and from notifications. It stays open, readable, searchable and answerable. kith never leaves the Matrix room, and nobody else in the room can tell.

This differs from archiving. An archived room stays in its space and only stops counting toward unread totals. Spam means the conversation should not have arrived at all.

## Marking a room by hand

| Where | Key / command | Effect |
| --- | --- | --- |
| Room list | `!` | Move the room under the cursor into Spam, or take it out |
| Composer | `/spam` | The same, for the room you are writing in |

A **Spam** group appears in the rail whenever at least one room is in it, and disappears when it is empty. Like other built-in groups, its rail key is `spam`, and you can rename, reorder or hide it in `[display.rail]`.

Marking a room adds its ID to `[spam] rooms`. **Releasing a room writes an exemption**, instead of just removing it from a list, and it writes it in two places:

- **In the room's account data**, where the release replaces whatever verdict a rule left there. The room leaves Spam on every machine your account runs on, and no rule on any of them can move it back.
- **In `[spam] except`** on this machine, so the release holds here even before anything reaches the homeserver, or with no homeserver at all.

Without the exemption, anyone patient enough to send some harmless messages after their pitch could get back into your list, and Spam would become a second inbox you have to check. If the account data cannot be written, the status line says so. The room is released on this machine, but your other machines still show it in Spam.

Marking a room with `!` writes only `[spam] rooms`. The rooms you mark by hand are part of this machine's config and do not follow the account.

## Configuration

```toml
[spam]
rooms  = []          # rooms you put in Spam
except = []          # exemptions; these outrank everything
first_message = true
direct        = true
ratio  = 0.0         # 0 = the ratio rule is off
floor  = 5
window = "168h"
```

`rooms` and `except` use the place vocabulary shared by every list in the config:

| Entry | Matches |
| --- | --- |
| `!abc:example.org` | That room, by ID |
| `room:<name>` | The room shown under that name |
| `space:<name>` | Every room in that space |
| `protocol:<network>` | Every room on that network, e.g. `protocol:Google Messages` |
| `dm` / `group` | Every direct message, or every room that is not one |

A bare word that is not a room ID is refused at startup. For example, `rooms = ["protocol:Google Messages"]` sends everything arriving over one SMS bridge to Spam.

## Filters

Filters identify spam **messages**. Nothing is caught automatically until at least one `[[spam.filter]]` exists, so the filter list is the on switch.

```toml
[[spam.filter]]
name  = "crypto"
words = ["*bitcoin*", "*binance*", "invest*"]

[[spam.filter]]
name  = "that number"
from  = "@whatsapp_15550100:example.org"   # everything this sender writes
```

| Key | Meaning |
| --- | --- |
| `name` | Required. The filter name appears in answers to "why is this room in Spam?" |
| `words` | Words to match, with the same globs as tracked words: `pay` matches the whole word, `pay*` words that start with it, `*pay` words that end with it, `*pay*` anywhere. Case is ignored |
| `from` | Only messages from this MXID. With no words (or only `*`), the filter catches everything they write |

A filter needs `words`, `from` or both. A filter with neither is refused, because it would catch everything. When several filters match a message, the first one in the file is credited.

### Preview before you trust a filter

`/caught` in the composer (or `:caught` on the command line) lists the messages your filters catch, searched in the local message index. It uses the same matcher the daemon uses, so the preview and the real catch agree. From a room it starts with that room and `tab` widens the scope. From the rail it starts with everything.

Use it every time you add a word. `invest*` looks harmless, but it also catches "investigated". The preview covers word filters only. Filters that match by sender alone are counted in the summary but not listed.

## The three promotion rules

Filters judge messages. These rules decide when a caught message moves its **room** into Spam. Each rule has its own switch, and they are checked in this order:

| Rule | Switch | Fires when |
| --- | --- | --- |
| First message | `first_message` (default `true`) | The first message in a room is caught. In an established room one spam message is noise. In a new room, the first message is the whole relationship |
| Direct message | `direct` (default `true`) | A message in a direct message is caught **and you have never written in that DM**. A DM is one person, so a verdict on the message is a verdict on the room. This rule never applies to groups, so one spammer among four hundred members cannot move a group |
| Mostly spam | `ratio`, `floor`, `window` (off by default) | Within `window`, at least `floor` messages were sent and at least `ratio` of them (0–1) were caught. This is the only rule that can move a room that was fine yesterday, which is why it is off by default |

```toml
[spam]
ratio  = 0.6      # three messages in five
floor  = 5        # fewer than five messages is not evidence
window = "72h"
```

A room with an exemption in `except`, or one you released on any of your machines, is never promoted. A room whose history cannot be read from the cache is not promoted either, because the two strongest rules rely on knowing that a room is new.

**Rules promote; only you demote.** No rule ever takes a room out of Spam. A room's verdict never changes back on its own, so a room that crosses the ratio twice is still promoted only once. A release is permanent on every machine: before a rule's verdict is written, the daemon checks the homeserver, and a room released elsewhere a moment ago is left alone.

The two exemptions differ in reach. `[spam] except` belongs to one machine and outranks everything there, including `[spam] rooms`. A release in account data belongs to the account and outranks every rule, but a room that this machine's `[spam] rooms` names (for example through `protocol:`) stays in Spam on this machine.

## Why is this room in Spam?

In a caught room, `W` (or `/why`) starts with the reason and the filter:

- "you marked it as spam"
- "the first message in it was caught by a filter (crypto)"
- "it is a direct message and its message was caught by a filter (crypto)"
- "most of what is said in it is caught by a filter (crypto)"

It then shows the [notification rules](notifications.md#why-was-this-silenced) that would apply once the room is released.

## Where verdicts are stored

The daemon runs the rules as messages arrive, whether or not a terminal is open. That matters most for the first-message rule, which usually fires at a time when nobody is looking.

A rule's verdict is written to the room's **account data** under the event type `org.kith.spam` (the rule, the filter and a timestamp), and then mirrored into the local cache. Releasing the room replaces it in the same place with `{"rule": 0, "released": true, "at_ms": …}`. A version of kith from before releases were stored reads that as "not in Spam", since rule 0 has always meant that. Account data lives on your homeserver, which has two consequences:

- **It survives a cache rebuild.** Each rule fires once, on a message that has already gone by. A verdict kept only in the cache would be lost with it, and nothing would ever catch that room again.
- **It follows the account.** Your other machines running kith see the same verdict and the same release. A running daemon picks up a release made elsewhere within a minute. Other people in the room never see either one.

Your own `[spam] rooms` and `except` lists are part of `config.toml`, so they are stored on each machine along with the rest of your configuration. A release is written to both `except` and the account data. A room you mark by hand is written only to `rooms`.

A caught room is excluded from notifications in the daemon before any notification rule is consulted. [Verification-code auto-copy](notifications.md#verification-codes-to-the-clipboard) still works in a caught room, because copying a code you asked for does not interrupt you.
