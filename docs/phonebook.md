# Phonebook

The phonebook is a folder of Markdown files, one per person, that kith reads and writes
and that any other program can read and write too: a mail client, a notes app, an
Obsidian vault. kith does not own it and keeps nothing of its own in it. The format is
plain YAML front matter, so agreeing on it is the whole protocol.

## Where it is

`[phonebook] dir` in the config, `~` expanded. Unset, it is `$XDG_DATA_HOME/phonebook`
(normally `~/.local/share/phonebook`). Point it at a folder of your vault to keep the
cards with your notes.

## A card

```markdown
---
aliases: [Dana Levi, Dana]
phone: ["+972541234567"]
email: [dana@example.com]
matrix: ["@dana:example.org"]
telegram: ["553183378"]
birthday: 1990-05-04
spouse: "[[Eli Levi]]"
children: ["[[Noa Levi]]", "[[Tom Levi]]"]
pets: [Rex]
origin: Haifa
tags: [person]
---
Anything written here is the person's notes. kith never reads or changes it.
```

**Who it is about** is said by these keys, each a list:

| key | holds |
| --- | --- |
| `phone` | numbers in international form; matched by their digits, so `+972 54-123-4567` and `972541234567` are one number |
| `email` | addresses, matched in any case |
| `matrix` | Matrix user IDs, `@user:server` |
| `telegram` | Telegram user IDs |
| `whatsapp` | WhatsApp IDs that are not a number (a number goes under `phone`) |
| `slack` | Slack users as `team.user` |

A card is found by any of these, never by its file name: renaming the file, or moving
the person's name around in it, breaks nothing. When two cards share an identifier,
the first by file name is used.

**Every other key is a field**: `birthday`, `spouse`, `children`, `pets`, `origin`, or
anything you name. A field is one value or a list of them; `[[links]]` to other notes
are kept as written. `aliases` and `tags` are fields like any other, which Obsidian
reads as the person's other names and tags.

## In kith

`I` opens a person's card: on a message, its sender's; in the member list (`p`), the
person under the cursor's; in the room list or a room that is a DM, or has one person
in it besides you and bots, that person's. `:person` does the same for what you are
on.

The card lists its identifiers, then its fields, then **Add a field**. Enter on a
field edits it: commas between values make it a list, and clearing it removes the key.
Add a field offers the keys the folder's other cards already use, then **New field**
to name one.

A person with no card gets one the first time a field is written: a file named after
them (numbered if the name is taken) holding every identifier kith knows of theirs and
`tags: [person]`. What kith knows a person by is every ID the people directory links
to them and every ID of their `[[display.identity]]`.

A change rewrites the card's front matter with the one key changed and the others kept
in their order, with their comments; the body is kept byte for byte. The file is
replaced through a rename, so a program reading it never sees half of it. Cards are
read afresh each time, so what another program wrote shows at once.

## For an assistant

`kith-mcp` hands an assistant a person's fields in `find_people`, and only when
`[agent.read] people = true` (off by default). The body never goes out.
