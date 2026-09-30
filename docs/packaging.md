# Packaging and releases

This page is for maintainers. It covers what one release tag produces, what each
publisher needs set up before it can publish, how to cut a release, and how to test
the packages without publishing anything. Users want
[getting-started.md](getting-started.md#install) instead.

## What a tag produces

Pushing a `v*` tag runs `.github/workflows/release.yml`. It re-runs every CI gate
against the tagged commit, then runs GoReleaser (`.goreleaser.yaml`), which produces
all of the following from that one tag:

| Artifact | Where it goes | Built by |
| --- | --- | --- |
| `kith_<ver>_{linux,darwin,freebsd}_{amd64,arm64}.tar.gz` | GitHub release | `archives` |
| `kith_<ver>_source.tar.gz` | GitHub release | `source` |
| `.deb`, `.rpm`, `.apk`, `.pkg.tar.zst` for amd64 and arm64 | GitHub release | `nfpms` |
| `checksums.txt` with a keyless cosign signature, one SBOM per archive | GitHub release | `checksum`, `signs`, `sboms` |
| Build attestations for archives, SBOMs and packages | the repository's attestation store | `attest-build-provenance` step |
| `kith-bin` PKGBUILD (prebuilt) | AUR | `aurs` |
| `kith` PKGBUILD (from the source tarball) | AUR | `aur_sources` |
| `Casks/kith.rb` | `EugeneShtoka/homebrew-tap` | `homebrew_casks` |

Two more ship from the repository rather than from the release:

| What | Where |
| --- | --- |
| `kith-git`, built from `main` | `packaging/arch/PKGBUILD`, pushed to the AUR by hand |
| The Nix flake | `flake.nix` and `packaging/nix/`, used straight from GitHub |

Prereleases (tags with a suffix, such as `v1.2.0-rc1`) get the GitHub release and its
assets, but the AUR and Homebrew publishers skip them (`skip_upload: auto`).

### Package contents

Every Linux package, the AUR packages and the Nix package install the same set:

| Installed path | Source in the repository |
| --- | --- |
| `/usr/bin/kith`, `kithd`, `kith-mcp` | the three `cmd/` binaries, built with `-tags=goolm` and `CGO_ENABLED=0` |
| `/usr/lib/systemd/user/kithd.service`, `kithd@.service` | `packaging/systemd/`, rendered by `scripts/render-units.sh` with `ExecStart=/usr/bin/kithd` |
| `/usr/share/applications/kith.desktop` | `packaging/desktop/kith.desktop` |
| `/usr/share/doc/kith/` | `README.md`, `ARCHITECTURE.md`, `docs/*.md`, and `config.toml.example` from `kith --print-config` |
| `/usr/share/licenses/kith/LICENSE` (`/usr/share/doc/kith/copyright` in the `.deb`) | `LICENSE` |

The Nix package uses `$out/...` for the same paths, with the units pointing into the
store. Before GoReleaser packages anything, its `before` hook runs
`scripts/prepare-packaging.sh`. The script writes the rendered units and the config
example into `build/packaging/`, which is ignored by git.

No package enables or starts anything. The daemon is a systemd *user* unit, and
package scripts run as root, outside any user's session. The post-install script
(`packaging/nfpm/postinstall.sh`, and `packaging/arch/kith.install` for the AUR)
only prints the commands to run.

### How packaged units get their directories

The unit's sandbox (`ProtectSystem=strict`, `ProtectHome=read-only`) allows writes to
four directories through `ReadWritePaths=`. systemd builds that mount namespace
before the daemon runs, and it fails the unit with `226/NAMESPACE` if one of them is
missing. Until now `make deploy` created them, which a package can't do: it has no
user to create them for.

The units now create them themselves:

- `%t/kith`, the runtime directory: `RuntimeDirectory=kith`, as before.
- `~/.local/share/kith`, `~/.local/state/kith` and `~/.cache/kith`:
  `ExecStartPre=+mkdir -p …`.

The `+` prefix runs that one command without the sandbox. A plain or `-`-prefixed
`ExecStartPre=` shares `ExecStart=`'s namespace and fails the same way. That was
tested with transient units, and so was the `+` form. In a user manager `+` grants
nothing, since there are no privileges to hand out, and `UMask=0077` still applies,
so new directories are mode 0700.

The obvious systemd-native alternatives don't cover it:

- There is no `*Directory=` directive for `~/.local/share`.
- `StateDirectory=` and `CacheDirectory=` would cover two of the three, but older
  user managers put `StateDirectory=` under `~/.config`. The paths would then depend
  on the systemd version.

`scripts/unit-parity-check.sh` (`make unit-check`) fails if a `ReadWritePaths=` entry
outside `%t` is missing from the `mkdir` line, so a new directory can't reintroduce
the problem. It still checks that the plain and templated units agree. The Nix
package rewrites the bare `mkdir` to a store path, because NixOS has no `/usr/bin`.

## One-time setup per publisher

Every publisher other than the GitHub release is optional. When its secret is
missing, GoReleaser still generates the file into `dist/` and skips the push. A
release without the secrets works; it publishes less.

### GitHub release

The job uses the workflow's own `GITHUB_TOKEN`, with `contents: write`,
`id-token: write` and `attestations: write` granted to that job only. It runs in the
`release` environment, which GitHub creates on the first run with no protection. In
**Settings → Environments → release**, add yourself as a required reviewer and limit
deployment to tags matching `v*`, so a pushed tag waits for approval before anything is
signed. The job also refuses a tag whose commit is not on `main`.

### AUR (`kith-bin`, `kith`, `kith-git`)

1. Create an account on <https://aur.archlinux.org> and add an SSH public key to it
   ("My Account" → "SSH Public Key"). Use a key made for this purpose only:

   ```sh
   ssh-keygen -t ed25519 -C "kith AUR release" -f aur_kith -N ''
   ```

2. Create the packages by pushing a first commit to each name. The AUR creates a
   package on its first push, and GoReleaser can do it for `kith-bin` and `kith`
   on the first stable release. `kith-git` has to be pushed by hand:

   ```sh
   git clone ssh://aur@aur.archlinux.org/kith-git.git
   cp packaging/arch/PKGBUILD packaging/arch/kith.install kith-git/
   cd kith-git && makepkg --printsrcinfo > .SRCINFO
   git add PKGBUILD kith.install .SRCINFO && git commit -m "Initial import" && git push
   ```

3. Add the private key as the repository secret `AUR_SSH_PRIVATE_KEY`
   (Settings → Secrets and variables → Actions), including the
   `-----BEGIN`/`-----END` lines.

`kith-git` computes its version from git, so it needs a push only when
`packaging/arch/PKGBUILD` itself changes.

### Homebrew tap

1. Create the public repository `EugeneShtoka/homebrew-tap`. The `homebrew-` prefix
   is what makes `brew install --cask eugeneshtoka/tap/kith` resolve. An empty
   `README.md` is enough; GoReleaser creates `Casks/kith.rb`.
2. Create a fine-grained personal access token with access to that repository only,
   and the permission **Contents: Read and write**. `GITHUB_TOKEN` can't be used,
   because it can't push to another repository.
3. Add it as the repository secret `HOMEBREW_TAP_GITHUB_TOKEN`.

It is a cask, not a formula. GoReleaser v2.10 deprecated `brews` for prebuilt
binaries in favor of `homebrew_casks`. The cost is that a cask has no `service`
block, so `brew services` can't manage the daemon. The cask points users at the
LaunchAgent in the archive through its caveats, and `uninstall launchctl:` unloads it
again. The binaries aren't signed or notarized. The cask's post-install hook clears
the quarantine attribute so Gatekeeper runs them.

### Nix

Nothing to publish: `flake.nix` is used directly as `github:EugeneShtoka/kith`.
Two things need looking after, and the `nix` CI job (`make nix-check`) fails when
either goes stale:

- **`vendorHash`** in `packaging/nix/package.nix` is the hash of the Go modules
  `go.mod` names. It changes with every dependency bump, Dependabot's included. On a
  mismatch the job's log shows the right value as `got:`; paste it in. Locally,
  `make nix-check` builds the flake when nix is installed and is skipped otherwise.
- **`flake.lock`** pins nixpkgs, so users get the Go and toolchain the flake was
  tested with. Refresh it with `nix flake update`, and let the job build it.

The nixpkgs Go must be at least the `go` line of `go.mod` (1.26.3). Nix builds with
`GOTOOLCHAIN=local`, so an older Go fails with a clear message rather than
downloading a toolchain.

### Debian, Fedora, Alpine and Arch repositories

Not set up. The `.deb`, `.rpm`, `.apk` and `.pkg.tar.zst` files are attached to the
GitHub release, and users install them from a file. A real package repository (an apt
repository on GitHub Pages, a Fedora COPR, or a signed Alpine repository) would
need a signing key and hosting. Add it when someone asks for automatic updates.
The `.apk` is unsigned, which is why the install instructions use
`--allow-untrusted`.

## Cutting a release

1. Make sure `main` is green, and check whether a newer GoReleaser v2 exists. The
   version is pinned in `release.yml` and in CI's `release-config` job; bump both
   together.
2. Tag and push:

   ```sh
   git tag -a v1.2.0 -m "v1.2.0"
   git push origin v1.2.0
   ```

3. Watch the Release workflow. When it finishes, check:
   - the release page has six archives, eight Linux packages, the source tarball,
     `checksums.txt` with its `checksums.txt.sigstore.json` bundle, and the SBOMs;
   - <https://aur.archlinux.org/packages/kith-bin> and `/packages/kith` show the
     new version (if `AUR_SSH_PRIVATE_KEY` is set);
   - `EugeneShtoka/homebrew-tap` has a commit updating `Casks/kith.rb` (if
     `HOMEBREW_TAP_GITHUB_TOKEN` is set).
4. If `go.mod` changed since the last release, update `vendorHash` (see
   [Nix](#nix)).

## Testing packages locally

Nothing here publishes anything or touches your installed daemon.

GoReleaser stays outside `go.mod` (its dependency tree is large). The pinned release,
checked against its SHA-256, is installed into `.tools/bin` by the gate that uses it:

```sh
make release-check   # installs goreleaser if needed, then: goreleaser check
```

That is the same target CI's `release-config` job runs. Afterwards `.tools/bin/goreleaser`
is on hand for the commands below.

Build everything into `dist/` without a tag. `--skip=sign,sbom` avoids needing cosign
and syft. GoReleaser needs a git repository with a remote, so run this from a clone:

```sh
.tools/bin/goreleaser release --snapshot --clean --skip=sign,sbom
ls dist/                                  # archives, packages, dist/aur/, dist/homebrew/
```

Inspect a package without installing it:

```sh
bsdtar -tvf dist/kith-*-x86_64.pkg.tar.zst
bsdtar -xOf dist/kith_*_amd64.deb control.tar.gz | tar -xzO ./control
bsdtar -xOf dist/kith-*-x86_64.pkg.tar.zst usr/lib/systemd/user/kithd.service | grep ^Exec
```

Build the generated AUR packages with makepkg, in a scratch directory:

```sh
mkdir /tmp/aur-bin && cd /tmp/aur-bin
cp ~/src/kith/dist/aur/kith-bin.pkgbuild PKGBUILD
cp ~/src/kith/packaging/arch/kith.install .
# makepkg uses a local file of the name it expects instead of downloading it:
cp ~/src/kith/dist/kith_*_linux_amd64.tar.gz "kith-bin_<pkgver>_x86_64.tar.gz"
makepkg -f          # builds ./kith-bin-*.pkg.tar.zst; -f without -i installs nothing
```

For the `kith-git` PKGBUILD:

```sh
cd packaging/arch
bash -n PKGBUILD && makepkg --printsrcinfo
makepkg -f          # clones main, builds, packages; installs nothing
```

To try a packaged unit against a real systemd without installing the package, put it
in a scratch directory and run `systemd-analyze verify --user` on it. The only
complaint should be that `/usr/bin/kithd` doesn't exist.

The Makefile's install paths can all be pointed into a scratch directory too:

```sh
make -n deploy PROFILE=work                        # print, don't run
make install PREFIX=/tmp/mx UNITDIR=/tmp/mx/units APPDIR=/tmp/mx/apps
```

`make install` still runs `systemctl --user daemon-reload` and `xdg-mime default`,
which act on your real session. Put no-op `systemctl` and `xdg-mime` scripts first
on `PATH` to avoid that.

## Platforms not packaged

### Windows

Not built. A port is being assessed separately. `.goreleaser.yaml` has a marked
**WINDOWS slot** listing what the release needs once the code supports it: a `zip`
format override, the Unix service files kept out of the archive, and optionally a
scoop or winget publisher.

### FreeBSD

Tarballs only: there is no systemd, and no FreeBSD port or package is maintained.
The release build is cgo-free, and the keyring library reaches the Secret Service on
FreeBSD only in cgo builds. So a FreeBSD user needs `allow_token_file = true` and
gets no end-to-end encryption. A FreeBSD port that builds natively with cgo would
lift that. It would be the thing to write if FreeBSD users turn up.

### Flatpak

Not packaged, on purpose. A Flatpak is a poor fit for this program, and the manifest
would give a worse result than the tarball:

- **There is no window.** `kith` runs inside your terminal. Flatpak can run it with
  `flatpak run` from a shell, but that gives you a longer command, nothing to launch
  from a desktop menu, and a `matrix:` handler that has to start a terminal from
  inside the sandbox. `--open` would need a host-spawn escape to do that.
- **The daemon is a systemd user service.** Flatpak has no way to install or enable
  one. The sandbox would have to be told to start `kithd` itself, from a separate
  autostart entry, on every login.
- **The client, the daemon and `kith-mcp` share a unix socket in
  `$XDG_RUNTIME_DIR/kith`.** Every process, including the assistant host's
  `kith-mcp`, would have to run inside the same app sandbox or reach it through a
  `--filesystem=xdg-run/kith` hole. `kith-mcp` is started by the assistant's host
  application, which is usually not inside that sandbox.
- **It runs your programs.** Your `[commands]` scripts, `[notifications] command`,
  mpv, hunspell, `llama-server` and `$EDITOR` are all host programs. Inside the
  sandbox each one needs `flatpak-spawn --host`, which removes the isolation that
  would justify the sandbox in the first place.

The parts a sandbox would help with (the keyring over the Secret Service portal,
notifications, the file chooser portal) already go through D-Bus interfaces that work
the same outside it. Revisit this only if kith gains a GUI.
