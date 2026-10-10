# WhatsApp

kith can link to WhatsApp directly: it becomes one of your phone's linked devices,
like WhatsApp Web, with no Matrix bridge in between. WhatsApp rooms sit beside your
Matrix rooms in the same client, or on their own: Matrix is optional.

## Before you link

- **WhatsApp's terms do not allow unofficial clients.** Accounts are rarely, but
  sometimes, banned for using one. kith links the way WhatsApp Web does and sends
  no more than a person would, but the risk is yours to take.
- **The phone has to come online every two weeks or so.** WhatsApp unlinks the
  devices of a phone that stays offline longer; link again then.
- **Link an account here or through a Matrix bridge, not both.** Both at once show
  every chat twice. To move from a bridge, log the bridge out of that number first.

## Setting up

The quickest way is inside kith: `:login whatsapp` asks for the number and a name
(suggested from the number's country), writes the account into the config, and
shows the pairing code to type on the phone. An account set up already is offered to link again.

By hand, in the config:

WhatsApp runs when the config has an account; there is no switch to turn on.

```toml
[[whatsapp.account]]
name = "personal"            # what `kith login whatsapp` takes
phone = "+44 7700 900000"   # international; "+" and spaces are fine
```

Then link:

```sh
kith login whatsapp personal
```

kith starts the daemon if it is not running and prints an eight-character code. On
the phone: WhatsApp → Settings → Linked devices → Link a device → **Link with phone
number instead**, and type the code. The command says when the phone accepted.
Groups and channels appear at once; history arrives over the next minutes. With one
account, the name can be left out.

Accounts can be added, removed and linked while the daemon runs: `kith login whatsapp`
makes it re-read the config first.

### Several accounts

Add one `[[whatsapp.account]]` per number and link each by name. Each account's
chats, communities and channels are its own: a group two of your numbers are both
in shows as two rooms, one per account.

### WhatsApp without Matrix

Leave `homeserver` and `user` empty and enable `[whatsapp]`: kith runs with WhatsApp
alone. An account that is not linked yet is named on the status line, with the
command that links it.

## What works

| | |
| --- | --- |
| Direct chats and groups | live, sending, history (the cache keeps every message, unless `[storage] messages_per_room` says fewer) |
| Replies, mentions | both ways; a reply to a message older than the cache keeps still shows what it quotes |
| Formatting | WhatsApp's `*bold*`, `_italic_`, `~strike~`, code, both ways |
| Edits, deletions, reactions | both ways |
| Photos, files, voice messages | shown and sent |
| Read state and typing | both ways; marking read on the phone clears kith's count |
| Archive | a chat archived on the phone is in kith's Archived tag; archiving in kith archives it on the phone when `[whatsapp.archive] mirror` is on ([configuration.md](configuration.md#follow-a-networks-own-archive)) |
| Communities | a space holding its groups; what is in it is the admins' to decide, so kith does not offer to file rooms into it |
| Channels you follow | rooms; the latest posts are fetched the first time; only a channel's admins post |

Not yet: reacting to channel posts and sending channel read receipts (kith keeps
read state there for itself), calls, status updates, votes cast before kith was
linked (a poll counts the votes it hears from then on), disappearing-message settings.

A poll shows its answers and how the votes stand as they arrive, your own marked; `P`
votes, or takes the vote back, and lists who chose each answer.

## Unlinking

On the phone, Linked devices lists kith as "Chrome (Linux)" (or your system's name):
unlink it there. Then remove `[[whatsapp.account]]` (or set `enabled = false` and
restart the daemon). The rooms stay in the cache, readable, until you clear it.

## When something is off

- **"not linked yet" on the status line** — run `kith login whatsapp <name>`.
- **"the phone unlinked kith"** — the phone, or WhatsApp, removed the device (often
  after two weeks offline): link again.
- **The code was typed on the wrong phone** — kith unlinks that phone again and says
  so; type it on the number the config names.
- **Rooms or members are a little behind** — kith lists an account's groups at most
  every 30 seconds (WhatsApp refuses faster listings); changes in between arrive with
  the next listing.

The daemon's log says more: `journalctl --user -u kithd -f` (or the unit kith wrote
for your config, `kithd-<instance>`).
