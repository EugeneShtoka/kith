# Clients on other machines

kithd can serve clients on other machines as well as the socket on its own: it
listens on the addresses you name, over TLS 1.3, and serves only the devices paired
with it. It is off by default.

This is being built. kithd listens and pairs devices now; the client that dials a
remote kithd, and pairing by QR code, come next.

## Device IDs

Every kithd and every machine a client runs on has a **device key**, made the first
time it is needed and kept in the OS keyring (or, only with `allow_token_file = true`
and no keyring, in a 0600 file). A **device ID** names that key: 52 letters and
digits in groups of four, such as

```text
C523-XW2U-B4T2-RKIG-PNZM-42UB-FX3Z-OLGG-UHU5-KVZ4-3YQV-KQR7-KXKA
```

Case, dashes and spaces do not matter when you paste one. Pairing pins the key, not a
certificate: a device and kithd each check the other's ID on every connection.

## Listening

In the bridge's `config.toml`:

```toml
[remote]
listen = ["192.168.1.5:7443", "100.64.0.3:7443"]
```

kithd listens on exactly those addresses. Name the address of the network your
devices come from: a LAN address, or a tailnet one (Tailscale, say) to reach kithd
from anywhere without opening a port. `0.0.0.0:7443` and `[::]:7443` take every
interface, but only when written out: a bare `:7443` is refused. A change takes
effect when kithd re-reads the config (it notices an edit, and the settings screen
writes through it), with no restart.

If kithd has no device key (a locked keyring) or the device list does not read, it
listens nowhere and says why in its log; the socket on its own machine is served
either way. An address it cannot listen on is logged and skipped.

## Pairing a device

On the device, print its ID:

```sh
kith devices id
```

On the bridge's machine (or on a device already paired), pair it, with a name to
know it by:

```sh
kith devices add C523-XW2U-B4T2-… laptop
kith devices                # this bridge's own ID, and every paired device
kith devices remove laptop  # or its ID
```

**A paired device may do everything** the socket allows: read and send on every
account, change the config, pair and unpair other devices. Pair only your own.
Unpairing closes the device's open connections at once, and it cannot connect again.

The list lives in `~/.local/share/kith/devices-<hash>.toml`. It is not the config and
not the cache, so rebuilding the cache never unpairs anyone. You may edit it by hand;
the next change, listing or reload reads it, and a device you took out is cut off
then. One that does not read keeps kithd from listening until it is fixed.

## What an attacker on the network sees

A TLS 1.3 handshake that asks for a client certificate. A key that is not paired is
refused in the handshake, before any call is read. Before TLS, each address (an IPv6
/64 as one) gets a burst of 10 handshakes and then one every two seconds, at most 32
handshakes run at once, and one that stalls is dropped after 10 seconds. See
[SECURITY.md](../SECURITY.md#a-listening-kithd).
