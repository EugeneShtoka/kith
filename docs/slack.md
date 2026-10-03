# Slack

kith can sign in to Slack directly, as the Slack web client does: no Matrix bridge,
and nothing for the workspace's admins to install or approve. Slack workspaces sit
beside your other rooms in the same client, or on their own: Matrix is optional.

This is being built in steps. Today kith signs in and lists each workspace's
channels, direct messages and group DMs; messages, threads, reactions and files come
next, and this page will say when.

## Before you sign in

- **kith uses your browser's Slack session.** It signs in with the token and cookie a
  browser signed in to the workspace holds, so it acts as you, with your access. The
  two are kept in the system keyring, never in the config. Signing out of Slack in
  that browser ends kith's session too; sign in again then.
- **Some workspaces forbid clients Slack did not make.** Check your workspace's
  policy; the risk of breaking it is yours to take.
- **Sign in here or through a Matrix bridge, not both.** Both at once show every
  channel twice.

## Setting up

In the config:

```toml
[slack]
enabled = true

[[slack.account]]
name = "work"         # what `kith login slack` takes
workspace = "acme"    # acme.slack.com; the full address works too
```

`workspace` can also be the workspace's ID, the `T…` in a link from the Slack web
client (`https://app.slack.com/client/T0123456789/…`); paste the whole link if that
is what you have.

Then sign in:

```sh
kith login slack work
```

The command says where to copy two things from a browser signed in to the workspace:

1. **The token**, starting with `xoxc-`: open the developer tools (F12), and in the
   Console paste the line the command prints. It reads the token from the page's
   storage.
2. **The cookie `d`**, starting with `xoxd-`: in the developer tools, Application
   (Storage in Firefox) → Cookies → `https://app.slack.com`, the value of `d`. The
   page cannot read this cookie itself, so it is copied by hand.

Paste each when asked; neither is shown as you type. kith checks the session with
Slack, refuses one for another workspace than `workspace` says, and connects. With
one account, the name can be left out.

## Several workspaces

Each `[[slack.account]]` is one workspace, each signed in on its own:

```toml
[[slack.account]]
name = "work"
workspace = "acme"

[[slack.account]]
name = "club"
workspace = "chess-club"
```

An account added to the config needs no restart: `kith login slack <name>` has the
daemon re-read the config. Turning `[slack]` on or off takes a restart of kithd.

## What you see

- **Each workspace is a space** in the rail, holding its channels, DMs and group DMs.
  It is where those rooms belong, so a space-exclusive tag (Archived, in the starter
  config) leaves them there.
- **Channels** are named as in Slack, without the `#`. Private channels are there
  too; archived ones are not.
- **A DM** is named after the person; **a group DM** after its people's handles.
- The status line says when a workspace is not signed in, or when Slack has ended
  its session.
