# Telegram

kith logs in to Telegram directly, as a Telegram client of its own, with no Matrix
bridge. **This is being built**: an account can be configured and reports itself
logged out; logging in, chats and messages arrive in the next releases (the plan:
`notes/design-telegram.md`).

## Before you start

- **Log in here or through a Matrix bridge, not both.** Both at once show every chat
  twice.
- **Telegram tells your other sessions about the new login**, as it does for any
  client.
- **Secret chats are not shown.** They are end-to-end encrypted to one device and
  never synced to another.

## Setting up

Telegram runs when the config has an account; there is no switch to turn on. Inside
kith, `:login telegram` will set one up. By hand:

```toml
[[telegram.account]]
name  = "personal"          # what `kith login telegram` takes
phone = "+44 7700 900000"   # international; "+" and spaces are fine
```

## The app's ID and hash

Every Telegram client identifies as an app, registered at
[my.telegram.org](https://my.telegram.org) → API development tools. Logging in asks
for your own app's **api_id** and **api_hash** first; they are kept with the session
in the system keyring, never in the config. Left empty, the account will use kith's own
app — when your build carries it: release builds will, a build from source only when
`TELEGRAM_API_ID` and `TELEGRAM_API_HASH` are set while building. (Both arrive with
logging in.)
