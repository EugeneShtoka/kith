# Security Policy

## Reporting a vulnerability

**Please do not report security vulnerabilities through public GitHub issues.**

Report privately via GitHub's
[Private vulnerability reporting](https://github.com/EugeneShtoka/kith/security/advisories/new)
(the **Security → Report a vulnerability** button on the repository). This keeps
the report confidential until a fix is available.

Please include, as far as you can:

- the affected version (`kith --version`),
- a description of the issue and its impact,
- steps to reproduce or a proof of concept,
- any suggested remediation.

You can expect an initial acknowledgement within a few days. Once a fix is
released, we're happy to credit reporters who wish to be named.

## Scope and things to keep in mind

kith is a local, single-user Matrix client. Because it holds credentials and
end-to-end-encryption key material, a few areas are especially
security-relevant — please flag anything in these areas:

- **Access token & credentials at rest.** The client stores a Matrix access
  token (and device ID) in the OS keyring, or in a `0600` file only when
  `allow_token_file` is set. Report any path that leaks it, logs it, or
  writes it with overly-permissive file modes.
- **E2EE key material.** The Olm/Megolm crypto store (device keys, session keys,
  cross-signing keys) and the keyring-held pickle key that encrypts it are
  sensitive. Report anything that could exfiltrate them, weaken verification, or
  cause the client to send to unverified/unexpected devices when it shouldn't.
- **The daemon socket.** `kithd` serves every client over a unix socket under
  `$XDG_RUNTIME_DIR`, and its `0600` permissions are the only authorization. Report
  any way another local user could reach it, or any request that reaches beyond the
  account the daemon serves.
- **Assistant and model features.** `kith-mcp` lets an assistant read the cache and
  send or draft messages, within the rooms `[agent.read]` and `[agent.write]` allow; the
  optional model features send message context to a configured endpoint, within the
  rooms `[assist]` allows. Report anything that sends or reveals more than that
  configuration permits.
- **Homeserver trust & TLS.** Report any case where the client would talk to a
  homeserver over an unvalidated TLS connection, or follow a redirect/`.well-known`
  discovery to an unexpected host without the user's intent.
- **Untrusted event content.** Message bodies, room names, and member display
  names are attacker-controllable. Report any rendering path where such content
  could inject terminal escape sequences, corrupt the display, or trigger
  unintended behavior.
- **Secrets in logs.** Report any case where tokens, keys, or decrypted message
  content end up on stderr or in the daemon's journal
  (`journalctl --user -u kithd.service`).

## Supported versions

This is a pre-1.0 project; only the latest release is supported. Please upgrade
to the newest release before reporting an issue.
