# Instagram

kith reaches Instagram's direct messages directly: no Matrix bridge. kith signs in
with your browser's session, stays connected, and shows your threads in a space of
their own (`Instagram <name>`): a one-to-one thread by the other person's name, a
group by its title (or its people's names). Messages arrive as they are sent, older
ones are fetched as you scroll up, and when kith starts again it picks up what it
missed. Replies, mentions of you, shared posts, reels and stories (as their text and
address) and call notes show; an edited message shows its new text, and an unsent one
reads `(deleted)`, as on other networks.

Photos, videos, voice messages, stickers and GIFs show as attachments, as on other
networks: pictures inline, the rest opened or saved from the message. A message
carrying more than one shows the first and counts the others (`(+2 more)`). A photo
or video sent to be viewed once stays in the Instagram app. Instagram's links to
attachments expire after a while; while the account is connected, kith asks
Instagram for fresh ones for a thread's newest messages, and an older one then stops
loading.

You can write in a thread, reply, edit what you wrote (Instagram allows five edits,
within 15 minutes) and unsend it, react (one reaction per message: a new one replaces
yours) and take your reaction back. Reactions show as people make them, and so does
who is typing, both ways. Attaching a file uploads it to Instagram and sends it;
Instagram's attachments carry no text, so what you typed goes after it as a message of
its own. Instagram does not take videos uploaded from its web app for some accounts;
for those, sending a video fails.

A thread you read in kith stays read: kith keeps the read position itself, before
telling Instagram, and Instagram's own read marks only ever move it forward.
**Instagram has no private read.** Reading a thread in kith tells Instagram, and the
others see that you have seen it, even with `[display] send_receipts = false`. Marking
a thread unread is not supported yet.

## Before you sign in

- **kith signs in with the session your browser holds.** Signing in to Instagram with
  a password reaches a cookie-consent page kith cannot answer, so kith asks for the
  cookies your browser sends to `www.instagram.com` instead. They are a session:
  whoever has them can read and write as you. kith keeps them in the system keyring,
  never in the config.
- **Signing out of Instagram in that browser ends kith's session too.** Copy the
  cookies from a private window and close it without signing out.
- **Instagram does not offer this to third-party apps.** kith uses the same unofficial
  route as the Matrix bridge mautrix-meta. Instagram may restrict accounts it believes
  are automated; the risk is yours to take.
- **Sign in here or through a Matrix bridge, not both.** Both at once would show
  every chat twice.

## Setting up

Inside kith, `:login instagram` asks what to call the account, writes it into the
config, and then asks for the cookies. From a shell, `kith login instagram` does the
same.

To copy the cookies:

1. Open a private window and sign in at <https://www.instagram.com>.
2. Go to <https://www.instagram.com/direct/inbox/> and open the developer tools
   (F12) → Network, then reload the page.
3. Pick the first request to `www.instagram.com`. Under Request Headers, copy the
   value of **Cookie**. It holds `sessionid`, `csrftoken` and `ds_user_id`, the three
   kith needs.
4. Paste it at kith's prompt, then close the private window without signing out.

A "Copy as cURL" of the same request works too. `kith login instagram` in a shell
reads a line of at most about 4,000 characters, which a cURL can pass; then paste the
Cookie header, or just the three cookies kith needs, with their values from the
developer tools' Application → Cookies (Storage in Firefox):
`sessionid=…; csrftoken=…; ds_user_id=…`. Inside kith, `:login instagram` takes
either whole.

The config holds only the name:

```toml
[[instagram.account]]
name = "me"
```

`kith login instagram me` signs that account in again with fresh cookies.

## When the session ends

Instagram rotates the cookies as kith uses them, and kith saves each new set, so a
session lasts until you sign out of it or Instagram ends it. Then the account shows
as signed out with what to do: copy the cookies again and log in. When Instagram wants
a security check, or a cookie choice, finish it in your browser at instagram.com
first.

Signing out in kith (from the account's space) forgets the session here only, and
can forget the account's threads too. kith does not sign out of Instagram, because
that would end the session the cookies came from, which may be your browser's.
