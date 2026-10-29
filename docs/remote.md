# Clients on other machines

kithd can serve clients on other machines as well as the socket on its own: it
listens on the addresses you name, over TLS 1.3, and serves only the devices paired
with it. It is off by default.

This is being built. kithd listens and pairs devices now, by QR code or by pasted ID;
the client that dials a remote kithd comes next.

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

## Pairing by QR code

On a machine the bridge already trusts (its own, or a device already paired), start
a pairing:

```sh
kith pair            # or: kith pair laptop, to name the device here
```

It prints a QR code and the same as a line to paste, such as

```text
kith join 'kith://pair?addr=192.168.1.5%3A7443&exp=1793270400&id=C523-XW2U-…&token=K5QW…'
```

good once, for ten minutes. The URI carries the bridge's addresses (each `[remote]
listen` address; `0.0.0.0` or `[::]` becomes every address of the machine another
device could reach, not loopback or link-local), its device ID, the token and when it
expires. With `[remote] listen` empty there is nothing to dial, and `kith pair` says
so.

On the new device, scan it or paste the line:

```sh
kith join 'kith://pair?…'          # --name desk to be listed as desk, not the hostname
```

`kith join` dials the addresses in order with this machine's device key, and takes
only the bridge whose key has the ID in the URI: its first connection is already
authenticated, with nothing to trust on first use. While a pairing is open, the
bridge lets an unpaired key complete the handshake, but serves it only the one call
that answers a pairing, and only with the token.

Both screens then show six digits, worked out from both device IDs and the token.
Check they match, then answer `y` on the trusted machine. The device is paired (listed
as added by the device that confirmed it), and `kith join` writes the bridge into its
`device.toml`:

```toml
[[bridge]]
name      = "192.168.1.5"
addresses = ["192.168.1.5:7443", "100.64.0.3:7443"]
id        = "C523-XW2U-B4T2-RKIG-PNZM-42UB-FX3Z-OLGG-UHU5-KVZ4-3YQV-KQR7-KXKA"
```

`name` is the first address's host until you change it; joining the same bridge again
updates its addresses and keeps the name.

Nothing is paired if the digits differ (answer anything but `y`), if ten minutes pass,
or if either side goes away first (`kith pair` interrupted, `kith join` closed): the
token is spent either way, the waiting device is cut off, and `kith pair` makes a new
one. Only one device may answer a token; a second is refused.

## Pairing by pasted ID

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
refused in the handshake, before any call is read, except while a pairing is open, and
then it may answer the pairing and do nothing else. Before TLS, each address (an IPv6
/64 as one) gets a burst of 10 handshakes and then one every two seconds, at most 32
handshakes run at once, and one that stalls is dropped after 10 seconds. See
[SECURITY.md](../SECURITY.md#a-listening-kithd).
