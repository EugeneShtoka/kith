# Google Messages

kith is learning to reach Google Messages directly, as Messages for web does: paired
with the Messages app on your Android phone, with no Matrix bridge. **It is being
built.** Today kith pairs, stays connected, and shows your conversations in a space
of their own (`Google Messages <name>`): the inbox, with the newest messages of each
conversation that has something new, then every message as it arrives. People with a
phone number are named by their contact name and known by the number. Pictures,
videos, voice messages and files show as a short label (`[photo]`, `[file: cv.pdf]`)
for now, and an MMS the phone has not downloaded yet says so. Sending, older history
and reactions come next.

A conversation you read in kith stays read: kith keeps the read position itself,
before telling the phone, and keeps telling it until it hears. Conversations the phone
files as spam or blocked, and deleted ones, are not shown; archived ones are.
Signing out in kith (from the account's space) unpairs kith from the phone.

## Before you pair

- **The phone's Messages must use your Google account.** In Messages, tap your
  profile picture: Device pairing should offer pairing with the Google account.

- **The phone relays everything.** Messages for web has no copy of your messages of
  its own: every message goes through the phone. It has to be on and online; when it
  is not, the account says so and kith waits for it.
- **Only one Messages for web session is active at a time.** Pairing kith takes it
  over from a browser or a Matrix bridge, and opening Messages for web in a browser
  (or a bridge reconnecting) takes it back; the account then says another session is
  active. Pair kith here or use a bridge, not both.
- **Google does not offer this to third-party apps.** kith uses the same unofficial
  route as the Matrix bridge mautrix-gmessages. The risk is yours to take.

## Setting up

Inside kith, `:login gmessages` asks what to call the phone, writes it into the
config, and pairs it. From a shell, `kith login gmessages` does the same.

kith pairs with the Google account the phone's Messages uses (Google discontinued
pairing by QR code). In a browser, sign in at
<https://messages.google.com/web> with that account, open the developer tools (F12) →
Network, reload, pick any request to `messages.google.com`, and copy the value of
the **Cookie** request header. Paste it at kith's prompt. The phone then asks you to
confirm the pairing: tap the emoji kith shows. The cookies sign in to your Google
account, so kith keeps them only in the system keyring.

The config holds only the name:

```toml
[[gmessages.account]]
name = "phone"
```

`kith login gmessages phone` pairs that account again.

## When the pairing ends

kith keeps the pairing in the system keyring and saves it again whenever Google
refreshes it. Unpairing from the phone (Messages → Device pairing), or signing the
Google account out, ends it: the account shows as not paired, and
`kith login gmessages <name>` pairs it again.
