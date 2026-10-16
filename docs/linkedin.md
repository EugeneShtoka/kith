# LinkedIn

kith reaches LinkedIn's messages directly: no Matrix bridge. kith signs in with your
browser's session, stays connected, and shows your conversations in a space of their
own (`LinkedIn <name>`), with the messages LinkedIn sends with each conversation and
every new one as it arrives: replies, forwarded messages (quoted), shared posts (as
their address), edits and deleted messages. Pictures, GIFs, files, videos and voice
messages show as attachments, as on other networks: pictures inline, the rest opened
or saved from the message. A message carrying more than one names the others
(`[file: cv.pdf]`). LinkedIn's links to them expire after a while; an old one then
stops loading.

You can write in a conversation, reply (the reply quotes the original, as LinkedIn
shows it), mention people (`@` offers the conversation's members), edit and delete
your messages, react (reacting again with the same emoji takes it back), and scroll
back past what kith has: older messages are fetched from LinkedIn. Reactions show as
people make them, and so does who is typing. Attaching a file sends it with what you typed as its text: a video
as a video and audio as a voice message, anything else (pictures too, as LinkedIn's
web app sends them) as a file.

A conversation you read in kith stays read: kith keeps the read position itself,
before telling LinkedIn, and keeps telling LinkedIn until it hears. LinkedIn marks a
whole conversation read, not a message, and its own read times only ever move kith's
position forward. Conversations LinkedIn filed as spam are not shown.

## Before you sign in

- **kith signs in with the session your browser holds.** LinkedIn has no login an
  app can lead, so kith asks for the cookies your browser sends to
  `www.linkedin.com`. They are a session: whoever has them can read and write as you.
  kith keeps them in the system keyring, never in the config.
- **Signing out of LinkedIn in that browser ends kith's session too.** Copy the
  cookies from a private window and close it without signing out.
- **LinkedIn does not offer this to third-party apps.** kith uses the same unofficial
  route as the Matrix bridge mautrix-linkedin. LinkedIn may restrict accounts it
  believes are automated; the risk is yours to take.
- **Sign in here or through a Matrix bridge, not both.** Both at once would show
  every chat twice.

## Setting up

Inside kith, `:login linkedin` asks what to call the account, writes it into the
config, and then asks for the cookies. From a shell, `kith login linkedin` does the
same.

To copy the cookies:

1. Open a private window and sign in at <https://www.linkedin.com>.
2. Open the developer tools (F12) → Network, and reload the page.
3. Pick any request to `www.linkedin.com`. Under Request Headers, copy the value of
   **Cookie**. It holds `li_at` and `JSESSIONID`, the two kith needs.
4. Paste it at kith's prompt, then close the private window without signing out.

`kith login linkedin` in a shell reads a line of at most about 4,000 characters, and
the header can be longer. Then paste just the two cookies kith needs, with their
values from the developer tools' Application → Cookies (Storage in Firefox):
`li_at=…; JSESSIONID=…`. Inside kith, `:login linkedin` takes the whole header.

A "Copy as cURL" of the same request also works when it is on one line. It also
carries two headers LinkedIn's web app sends, which kith then uses instead of its
own.

The config holds only the name:

```toml
[[linkedin.account]]
name = "linkedin"
```

`kith login linkedin linkedin` signs that account in again with fresh cookies.

## When the session ends

LinkedIn rotates the cookies as kith uses them, and kith saves each new set, so a
session lasts until you sign out of it or LinkedIn ends it. Then the account shows
as signed out with what to do: copy the cookies again and log in. When LinkedIn wants
a check (it may take a new session for a robot), finish it in your browser at
linkedin.com first.

Leaving a conversation in kith (`L` in the room list) deletes it from your LinkedIn
inbox, as LinkedIn's own "Delete conversation" does: the others keep theirs, and a new
message in it brings it back.

Signing out in kith (from the account's space) forgets the session here only. kith
does not sign out of LinkedIn, because that would end the session the cookies came
from, which may be your browser's.
