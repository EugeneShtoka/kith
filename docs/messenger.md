# Messenger

kith is learning to sign in to Messenger directly, as the Messenger Lite app does: no
Matrix bridge. **It is being built.** Today kith signs in, stays connected, and shows
your threads in a space of their own (`Messenger <name>`): messages as they arrive,
older ones as you scroll up, and you can write and reply. Pictures, files and
stickers show as a short label (`[photo]`, `[file: plan.pdf]`) for now. Reactions,
typing, editing, sending files, and Instagram come next.

**End-to-end encrypted chats** (Messenger encrypts one-to-one chats by default since
late 2023) are read the way Messenger's own apps read them: kith registers itself
once as a device of your account, and Messenger lists it with your devices. Only
messages sent after that can be read; older encrypted history sits behind
Messenger's PIN-protected backup, which kith cannot open, and Meta keeps no older
pages of them to scroll back to. An encrypted chat is the same room as the thread it
was, named after its person. Because one room can hold both, kith counts every
Messenger room as encrypted: the assistant and kith-mcp leave them out unless you
set `encrypted = true` ([assist](assist.md), [mcp](mcp.md)).

A thread you read in kith stays read: kith keeps the read position itself, before
telling Meta, and keeps telling Meta until it hears. Meta's own read marks only ever
move it forward.

## Before you sign in

- **kith signs in with your Meta account's password.** It asks for your email or
  phone and password, then for whichever second factor your account has: a code
  from an authenticator app, by SMS or WhatsApp, a backup code, or approving the
  login in the Facebook or Messenger app. The password is used once and never kept;
  what kith keeps is the session Meta hands back, in the system keyring, never in
  the config.
- **A login from a new device can trigger a security check.** Meta may stop the
  login and ask you to confirm it's you. kith cannot answer that check: sign in once
  at facebook.com, finish it there, then log in here again.
- **Meta does not offer this to third-party apps.** kith uses the same unofficial
  route as the Matrix bridge mautrix-meta. Meta may restrict accounts it believes
  are automated; the risk is yours to take.
- **Sign in here or through a Matrix bridge, not both.** Both at once would show
  every chat twice.

## Setting up

Inside kith, `:login messenger` asks what to call the account, writes it into the
config, and then asks for your login one step at a time, drawing the password as
dots. From a shell, `kith login messenger` does the same.

The config holds only the name:

```toml
[[messenger.account]]
name = "meta"
```

`kith login messenger meta` signs that account in again, for example after
Meta ended the session.

## When the session ends

Meta rotates the session as kith uses it, and kith saves each new one, so a
session lasts until you sign out of it or Meta ends it. Then the
account shows as signed out with what to do: log in again, or, when Meta wants
a security check or wants you to accept its terms, do that at the address it gives
first.
