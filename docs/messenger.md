# Messenger

kith is learning to sign in to Messenger directly, as the Messenger Lite app
does: no Matrix bridge. **It is being built.** Today kith signs in, keeps the
session and stays connected, and the account's state shows with your other
networks; your conversations arrive in a later release, then sending, then
end-to-end encrypted chats, then files and images.

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
