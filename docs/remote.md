# Clients on other machines

kithd can serve clients on other machines as well as the socket on its own: it
listens on the addresses you name, over TLS 1.3, and serves only the devices paired
with it. It is off by default.

Once a machine has joined a bridge (`kith join`, below), `kith`, `kith-notify`,
`kith-mcp` and the desktop window there dial it instead of a kithd of their own.

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

## kithd's recovery key

A backup holds no device key, so a kithd restored on a new machine, or after its
keyring is reset, would be a new device that every device and bridge must pair again.
Its **recovery key** brings the key back: kithd's device key written out as 56 letters
and digits in groups of four, such as

```text
MZXW-6YTB-OI2D-…-K3QA
```

Write it down, or keep it in a password manager, away from the machine. Anyone who
has it can pose as this bridge, so keep it like a password.

- **When kithd's key is first made**, kith asks for it, at a terminal, before it
  starts kithd: on kith's first run on a machine, or `kith pair`, `kith login` or any
  command that starts kithd there. `Recovery key (Enter for a new key)`: type the old
  kithd's recovery key and kith shows the device ID it gives, the old one, so the
  devices a restored backup lists stay paired. Press Enter for a new key, and kith
  prints its recovery key once.
- **After `kith restore`** it asks too. If kithd made a key already (an install that
  started kithd before the restore), it names that key's ID, and a recovery key typed
  replaces it; Enter keeps it.
- **`kith devices recovery-key`** prints it again, with the ID. It works only on
  kithd's own machine, where it reads the keyring; no device can ask kithd for it,
  over the network or the socket.

kithd itself never asks. Without a terminal (a script, a unit), or with kithd already
running, kith asks nothing and kithd makes a new key when it first needs one, as
before. A recovery key with a character mistyped is refused as a typo, and kith asks
again; case, dashes, spaces and line breaks do not matter.

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

If kithd has no device key (a locked keyring), the device list does not read or its
mesh store does not open, it listens nowhere and says why in its log; the socket on its
own machine is served either way. An address it cannot listen on is logged and skipped.

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

`kith devices` lists each device with its role: `device` for a client, `bridge` for
another kithd of the mesh ([below](#bridges-in-a-mesh)).

The list is kept in the mesh store, `~/.local/share/kith/mesh-<hash>.db`: each change
is an entry signed with the bridge's device key, which the other bridges of its mesh
check. It is not the config and not the cache, so rebuilding the cache never unpairs
anyone. kithd writes the list out to `~/.local/share/kith/devices-<hash>.toml` after
every change. You may edit that file by hand. The next change, listing or reload reads
it, against what kithd last wrote there, into the store as changes of the bridge's
own (a change another bridge made meanwhile is not undone by a file that never showed
it):

- **An entry taken out unpairs the device**, which is cut off then. This holds even if
  the device was paired after you opened the file: removing is the safer mistake, and
  you can pair it again.
- **An entry put in pairs it**, and a changed `name` renames it. The `role` is the
  bridge's: changing it between `device` and `bridge` changes nothing.
- **A device unpaired before stays unpaired** even if the file lists it, so a stale copy
  of the file cannot let a lost laptop back in. Pair it again with
  `kith devices add`. kithd logs each one it left out, and writes the file again
  without it.

A file that does not read, or a mesh store that does not open (another key's, or one
written by a newer kith), keeps kithd from listening until it is fixed, and nothing is
written meanwhile. The first kithd of this release reads the file into a new store once,
keeping the file as it was in `devices-<hash>.toml.pre-mesh`, which nothing reads after.
A backup (`kith backup`) holds the store; restored where kithd keeps the same device
key, the same devices are paired. Onto a new machine, give `kith restore` the old
kithd's [recovery key](#kithds-recovery-key) when it asks, and kithd keeps that key.
If kithd's device key changes (a keyring reset, or a restore without the recovery
key), the store no longer lists it, and kithd does not listen. Move `mesh-<hash>.db`
aside, and the next start makes the store again from `devices-<hash>.toml`. After a
restore onto a new machine there is no such file, and the devices pin the old
machine's key anyway: pair each one again.

## Bridges in a mesh

Two or more kithd (a home server and a laptop, say) can pair with each other as a
**mesh**. For now the mesh carries the device list: a device paired with any bridge is
admitted by every bridge, and one unpaired on any bridge is cut off by every bridge.
Each bridge still runs its own network accounts and its own config. Two bridges on one
WhatsApp account keep a chat under the person's number as soon as either learns it: a
chat known by the person's LID merges into the number's on every bridge running that
account ([whatsapp.md](whatsapp.md#what-works)).

On a device the mesh trusts, run `kith pair` as for a device. On the new bridge's
machine, with its kithd set up (its own `config.toml`), run

```sh
kith join --bridge 'kith://pair?…'   # --name laptop to be listed as laptop
```

That machine's kithd answers the pairing with its own key, offering the addresses in
its `[remote] listen` (none: it only dials the others). It lists the devices paired
with it, which every bridge admits once it has joined, and shows six digits; `kith
pair` on the trusted side says a bridge asks to pair. Once confirmed there, the new
bridge takes the mesh's device list in place of its own, pairs its own devices again
into it, and `kith join` lists the members it admits from then on. If the confirmation
was given but its answer was lost, run the same line again: it takes the mesh's list
without asking a second time. A pairing refused, expired or abandoned changes nothing on
either bridge.

Each bridge dials every other bridge that has addresses, over the same pinned TLS as a
device, backing off from one second to five minutes while one does not answer; between
two that dial each other, one stream is kept. A bridge serves the sync only to a key
its list has as a bridge: a device's key, an unpaired one and the socket are refused.

```sh
kith mesh   # the other bridges: where each is dialed, the accounts it runs, last heard, lag, clock gap
```

- **Clocks.** Two bridges whose clocks are more than 60 seconds apart do not sync, and
  `kith mesh` names the gap; a change written by a clock further ahead waits until this
  bridge's clock reaches it. Set the clocks right (NTP).
- **Removing a bridge.** `kith devices remove <bridge>` on any bridge unpairs it
  everywhere. A change it made before it learned of its removal still counts on every
  bridge; once it knows, nothing it writes is taken. The removed bridge stops listening
  and syncs only to pass its removal on. `kith join --bridge` joins a mesh again.
- **A bridge back after weeks** gets the mesh's state whole and then sends what it wrote
  meanwhile; nothing is lost either way.
- **A forked bridge.** A bridge found to have signed two different changes under one
  number (its key copied, or a store restored from an old backup while it kept writing)
  is named in the log and in `kith mesh`, and removed from the mesh; pair it again.

## Using a bridge

A machine whose `device.toml` names a `[[bridge]]` dials it: `kith`, `kith-notify`,
`kith-mcp` and the desktop window all reach kithd there over TLS and never start one
here. The config is the
bridge's, read and changed through it (`:set`, the settings screen); `device.toml` is
this machine's, and so are the media cache, the log and the scripts in `commands/`
beside it. With several `[[bridge]]` entries the first is dialed.

- **Addresses in order.** Each address is tried in turn, and the first that answers
  with the bridge's own key is used. One that answers with another key is passed
  over, never trusted, so a bridge's LAN address and its Tailscale address can both
  be listed.
- **A network that changes.** A connection with nothing on it for 15 seconds is
  pinged; with no answer in 10 more it is dropped and a new one dialed, and the
  message stream resumes where it stood: nothing that arrived meanwhile is lost.
- **Links.** `kith --open` hands a link to a window on this machine, never to one
  open on another device; with none here it opens a terminal here.
- **What stays on the bridge's machine.** Restarting kithd (`:login` turning a
  network on), the phonebook folder (`[phonebook] dir`), a `[commands] dir` the
  config sets, and `kith --add-dictionary` are the bridge machine's; do them there.
- **The desktop window.** Its `kith-relay` dials the bridge the same way, with this
  machine's device key, which the window never sees. The profiles it offers are the
  bridge's config's; the one in use is this machine's (`device.toml`). The window
  says which bridge it is connected to, and, if the bridge no longer has this device
  paired, says the bridge refused it until it is paired again.

## What an attacker on the network sees

A TLS 1.3 handshake that asks for a client certificate. A key that is not paired is
refused in the handshake, before any call is read, except while a pairing is open, and
then it may answer the pairing and do nothing else. Before TLS, each address (an IPv6
/64 as one) gets a burst of 10 handshakes and then one every two seconds, at most 32
handshakes run at once, and one that stalls is dropped after 10 seconds. See
[SECURITY.md](../SECURITY.md#a-listening-kithd).
