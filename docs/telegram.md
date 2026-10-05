# Telegram

kith logs in to Telegram directly, as a Telegram client of its own, with no Matrix
bridge. **This is being built**: an account can be set up and logged in; its chats
are rooms in kith — private chats (the chat with yourself is Saved Messages),
groups, supergroups and channels, in a space named after the account ("Telegram
home") — and their messages arrive, live and as you scroll back, with their
formatting. Messages sent while kith was not running arrive when it connects again.
You can write in them: Markdown as Telegram's formatting, mentions, replies. Edits,
reactions and media come in the next releases (the plan:
`notes/design-telegram.md`); attachments read as a label for now ("[photo] …").
Chats in Telegram's Archived folder are listed too, not yet filed under kith's
Archived tag.

## Before you start

- **Log in here or through a Matrix bridge, not both.** Both at once show every chat
  twice.
- **Telegram tells your other sessions about the new login**, as it does for any
  client, and lists kith under Settings → Devices, where you can end its session.
- **Secret chats are not shown.** They are end-to-end encrypted to one device and
  never synced to another.

## Setting up

The quickest way is inside kith: `:login telegram` asks for the number, a name
(suggested from the number's country) and the app to log in through, writes the
account into the config, restarts the daemon if it ran no Telegram account yet, and
has Telegram send a code. It then asks for the code, and for the account's two-step
verification password when it has one; a wrong one is asked for again. An account set
up already is offered to log in again.

Telegram runs when the config has an account; there is no switch to turn on. By hand:

```toml
[[telegram.account]]
name  = "personal"          # what `kith login telegram` takes
phone = "+44 7700 900000"   # international; "+" and spaces are fine
```

then `kith login telegram personal` (see the [command-line reference](cli.md)).

## The app's ID and hash

Every Telegram client identifies as an app, registered at
[my.telegram.org](https://my.telegram.org) → API development tools: log in there with
the account's number and fill in the form (any title and short name; platform
Desktop). It shows the app's **api_id**, a number, and its **api_hash**, 32 letters
and digits. Logging in asks for them first; they are kept with the session in the
system keyring, never in the config.

Left empty, the account uses kith's own app — when your build carries it: release
builds do, a build from source only when `TELEGRAM_API_ID` and `TELEGRAM_API_HASH`
are set while building (`make build-daemon` reads them). Your own app is the safer
choice: Telegram may limit an app that many people share.

## What is kept

The session Telegram gives kith (whoever has it reads and writes as you), the app it
logged in through and your user ID are kept together in the system keyring, under the
account's number. The two-step password is checked by Telegram and never kept.
Logging in again replaces them.
