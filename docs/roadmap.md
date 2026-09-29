# Roadmap

What is planned or known to be missing, grouped by theme. Nothing here has a date:
items land when they are ready, each behind configuration that defaults to today's
behavior. If one matters to you, open a feature request and say how you would use it.

For what already exists, see the [documentation index](README.md).

## Accounts and devices

- A `logout` command that revokes the session on the homeserver and removes it from the keyring.
- Listing this account's other sessions, and renaming or signing out old ones.
- Verifying another person (in-room SAS verification); today only self-verification of your own sessions is supported.
- One view across several accounts: a unified room list, search and mentions over more than one daemon.

## Rooms and people

- Pinned messages: reading `m.room.pinned_events` and listing a room's pins.
- Ordering the people list by power level, so you can see who can act in a room.
- Presence (online, idle, last seen) for the people you talk to.
- An unread summary in the terminal window title.

## Messages and links

- Per-message delivery status from bridges that report one (sent to the remote network, failed there).
- Following bridge links (`wa.me`, `t.me`) to the bridged chat, the way `matrix:` and `matrix.to` links already work.

## Search

- Server-side search for history that was never cached locally.

## Platforms

- A launchd agent for running `kithd` on macOS; only systemd user units ship today.
- Native macOS notifications; today the desktop notification sink is D-Bus, and on macOS the notification command hook is the way to be notified.
