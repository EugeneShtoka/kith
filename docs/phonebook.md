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

The card lists its identifiers, then its fields, then **Everything they said** (every
message they wrote in a room you share; see [Search](search.md#everything-one-person-said)),
then **Add a field**. Enter on a
field edits it: commas between values make it a list, and clearing it removes the key.
Add a field offers the keys the folder's other cards already use, then **New field**
to name one.

A person with no card gets one the first time a field is written: a file named after
them (numbered if the name is taken) holding every identifier kith knows of theirs and
`tags: [person]`. What kith knows a person by is every ID the people directory links
to them and every ID of their `[[display.identity]]`.

### Searching by field

A **field term** is `key:value`: the key is any of a card's keys, in any case (identifier
keys too: `phone:4567`), and the value is part of any of its values, in any case; a list
matches when one of its items does. `role:attorney` finds a card with
`role: Family Attorney`. Several terms must all hold. A term is one word; the value is
matched as a piece, so `role:attorney` is enough for "family attorney".

- In the switcher (`ctrl+k`), a field term lists the people whose card meets it, by
  name, after the rooms that match the text. Choosing one opens your conversation with
  them: their DM or a room with them alone, on any network; else starts a DM where the
  switcher can; else opens their card.
- `:people` lists every card. Typed words narrow it by the card's name and `aliases`,
  field terms by its fields; `:people role:attorney` opens it narrowed. `enter` opens the
  card.
- In a search, `from:role:attorney` finds messages from everyone whose card meets the
  term, by every ID kith knows them by. A term no card meets searches nothing and says
  so. An ID after `from:` (`@dana:example.org`, `telegram:42`) is still an ID.
- A profile's `people.exclude` hides everyone whose card meets a term, there only
  ([configuration.md](configuration.md#people-in-a-profile)). There the value is
  matched word for word, not as a piece: `role:coll` hides nobody.

### A field beside names

`[[display.space_rule]] show_field = "company"` shows a field after names in that
space's or tag's room list, the one selected in the rail: a DM, or a room with one
person in it besides you and bots, reads "Dana Levi · Meta", and the member list (`p`)
shows it too. Settings (`,` → Names → Field beside names in …) offers the keys your
cards use. kith reads the members of the place's rooms once to find each room's person,
and again when a room's members change.

A change rewrites the card's front matter with the one key changed and the others kept
in their order, with their comments; the body is kept byte for byte. The file is
replaced through a rename, so a program reading it never sees half of it. A card is read
afresh each time it opens, so what another program wrote shows at once. The search and
the field beside names read the folder when the config changes, when kith writes a card,
when a card or `:people` opens, and when the room list refreshes.

## For an assistant

`kith-mcp` hands an assistant a person's fields in `find_people`, and only when
`[agent.read] people = true` (off by default); field terms in its query find people by
them only then. The body never goes out.
