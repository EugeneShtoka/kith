# kith documentation

kith is a terminal Matrix client made of three programs: `kithd`, a daemon that
owns your session, cache and encryption keys; `kith`, the terminal UI that attaches
to it; and `kith-mcp`, a Model Context Protocol server that lets an AI assistant read
(and, under your policy, write) through the same daemon.

New here? Start with [Getting started](getting-started.md), then take the
[tour of the client](usage.md).

## Using kith

| Page | What it covers |
| --- | --- |
| [Getting started](getting-started.md) | Requirements, installing, first login, running the daemon, profiles, where files live |
| [Using the client](usage.md) | The three panes, the rail and room list, the timeline, messages, threads, media, people and rooms |
| [Keybindings](keybindings.md) | Every default key by mode, and how to rebind them |
| [Search](search.md) | Offline full-text search, scopes and filters, the mentions, files, starred and tracked lists |

## Features in depth

| Page | What it covers |
| --- | --- |
| [Notifications](notifications.md) | Rules per room, space, sender and thread; quiet hours; do-not-disturb; burst folding; verification-code capture |
| [Spam](spam.md) | Content filters, promotion rules, the Spam group, `/caught` and `/why` |
| [Composer](composer.md) | Markdown, mentions, emoji, attachments, drafts, the send queue, scheduled messages, spelling and completion |
| [Assist](assist.md) | The local completion model and the optional remote model: `/summary`, `:todo`, thread names, privacy scoping |
| [Commands](commands.md) | Built-in `/` and `:` commands, and writing your own as executable scripts |
| [MCP server](mcp.md) | Connecting an AI assistant with `kith-mcp`: tools, read scope, the write policy and the audit ledger |
| [WhatsApp](whatsapp.md) | Linking WhatsApp directly: setup, several accounts, without Matrix, what works, the risks |
| [Encryption](encryption.md) | End-to-end encryption, device verification, key backup, key export and import, what is stored where |

## Reference

| Page | What it covers |
| --- | --- |
| [Configuration](configuration.md) | The config file, the settings screen, every section at a glance, themes, worked examples |
| [Command line](cli.md) | Every flag of `kith`, `kithd` and `kith-mcp`, plus the user-facing Makefile targets |
| [Troubleshooting](troubleshooting.md) | Common problems and the error messages that go with them |

The complete, annotated configuration reference is built into the client:

```sh
kith --print-config
```

## For contributors

| Page | What it covers |
| --- | --- |
| [Architecture](../ARCHITECTURE.md) | Layers, the `api.Backend` interface, the daemon wire, sync, enforced boundaries |
| [Database](database.md) | The SQLite cache, its schema, and how to change it safely |
| [Contributing](../CONTRIBUTING.md) | Setup, the `make check` gates, conventions, pull requests |
| [Packaging](packaging.md) | Cutting a release, what each package contains, the secrets and repositories each publisher needs, testing packages locally |
| [Roadmap](roadmap.md) | What is planned and not built yet |
| [Security](../SECURITY.md) | Reporting vulnerabilities, and what is in scope |
