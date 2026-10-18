# Signal

kith links to Signal directly: it becomes one of your phone's linked devices, as
Signal Desktop does, with no Matrix bridge in between. Your chats show in a space of
their own (`Signal <name>`): a one-to-one chat by the person's name, a group by its
title, and your notes to yourself as Note to Self. Messages arrive as they are sent.

**History.** When you link, your phone offers to transfer your message history. Accept
it and kith takes it once linked: your chats appear with their messages, read as far
as you had read them, quietly (nothing in it notifies). If kith stops part-way, it carries on the next time
it connects. Decline it,
and kith starts with only what arrives from then on, and a chat appears when its next
message does.

Replies, mentions, edits, deleted messages, reactions and who is typing show as they
do in Signal's apps, and so do messages written on your phone, and chats read there.
Pictures, videos, voice messages and files show as attachments, as on other networks:
pictures inline, the rest opened or saved from the message. A message carrying more
than one shows the first and counts the others (`(+2 more attachments)`). Signal keeps
attachments for a while only; an old one then stops loading.

You can write in a chat, reply, edit and delete what you wrote (for everyone), react,
and others see you typing. An `@name` you type goes as text, not yet as a mention.
Attaching a file sends it with what you typed as its caption; AAC audio goes as a
voice message, as Signal's apps record them.

A chat you read in kith stays read: kith keeps the read position itself, then tells
your phone and, with read receipts, the people who wrote. `[display] send_receipts =
false` tells your phone only.

**Disappearing messages.** kith keeps each chat's timer, and what you send there
carries it. Each message is deleted from kith once its timer has run, counted as
Signal's apps count it: from its sending for yours, from your reading (in kith or on
the phone) for others'. It is deleted as a deletion is: gone from the timeline and
search, or kept, with `[display.deleted] keep`, under `H`.

**Names.** Signal hides phone numbers unless someone shares theirs. kith names a person
by the name your phone's contacts give them (kith asks your phone for its contacts
each time it connects), else by their Signal profile name, which kith learns only once
they have written to you. When kith knows a person's number, it
joins them to the same person on your other networks (see
[Phonebook](phonebook.md)).

## Before you link

- **kith needs its Signal helper, `kith-signal`, which you build yourself.** Signal's
  encryption library is written in Rust, and linking it takes cgo, which the rest of
  kith does without. So it lives in a separate program that kithd starts when the
  config has a Signal account and finds the program beside kithd or on your `PATH`.
  No package includes it yet; see [Building kith-signal](#building-kith-signal).
- **Signal unlinks a device it has not seen for a while** (30 days, at the time of
  writing). Link again then.
- **Signal does not support third-party apps.** kith uses the same library as the
  Matrix bridge mautrix-signal. A change on Signal's side can stop it working until
  kith catches up.
- **Link here or through a Matrix bridge, not both.** Both at once would show every
  chat twice.

## Building kith-signal

It needs Go, git, Rust (`cargo`, from rustup), cmake, clang and protoc. From kith's
source:

```sh
make build-signal
install -m755 kith-signal "$(dirname "$(command -v kithd)")/"
systemctl --user restart kithd
```

`make build-signal` first builds libsignal at the version kith's Signal library
expects (this takes a while the first time; later builds reuse it), then writes
`./kith-signal`. Install it beside kithd, or anywhere on your `PATH`, and restart the
daemon: it starts the helper when there is a Signal account to run. Without the
helper, Signal stays off and your Signal chats stay readable from the cache.

kithd keeps the helper running: if it stops answering, kithd ends it and starts it
again, and the helper exits as soon as kithd goes away. Signal delivers again anything
the helper had not handed kithd yet, so nothing is lost in between.

## Setting up

Inside kith, `:login signal` asks what to call the account, writes it into the
config, and shows a QR code. On your phone, open Signal → Settings → Linked devices →
Link new device, and scan it. If the code does not scan from the terminal, kith also
shows the link it encodes, to draw as a QR code elsewhere. Your phone then lists kith
as `kith`. From a shell, `kith login signal` does the same.

The config holds only the name:

```toml
[[signal.account]]
name = "phone"
```

`kith login signal phone` links that account again.

## Unlinking

Unlink kith on your phone: Signal → Settings → Linked devices → `kith` → Unlink. The
account then shows as unlinked, with how to link it again, and what kith had cached
stays readable. Linking again makes kith a new device; accept the history transfer
again to bring what was said in between.

kith keeps the device's keys in `$XDG_DATA_HOME/kith/signal-<instance>.db`, encrypted
with a key kept in the system keyring (`<instance>|signal-store`).
