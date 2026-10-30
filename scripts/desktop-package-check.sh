#!/usr/bin/env bash
#
# desktop-package-check.sh — what a packaged desktop app holds, read from the package
# and not from forge.config.ts (desktop-check runs it on `electron-forge package`'s
# output, the release on each deb, rpm and zip it makes):
#
#   1. resources/kith-relay is the relay given, byte for byte, mode 0755: the app runs
#      the relay built with it, and no other;
#   2. the app is an ASAR alone (resources/app.asar, no resources/app/), which the
#      OnlyLoadAppFromAsar fuse needs;
#   3. the fuses: RunAsNode, NODE_OPTIONS and --inspect off, the embedded ASAR
#      integrity check and ASAR-only loading on (read with @electron/fuses, so it needs
#      desktop/node_modules);
#   4. nothing in the app is writable by group or others;
#   5. the deb installs chrome-sandbox setuid root, the sandbox Chromium falls back to
#      where unprivileged user namespaces are restricted (Ubuntu since 24.04). The rpm's
#      does not (electron-installer-redhat's spec copies without modes), so it leans on
#      user namespaces, which Fedora and openSUSE allow (docs/desktop.md).
#
#   scripts/desktop-package-check.sh <app dir | .deb | .rpm | .zip> <kith-relay>
#
# A package is unpacked into a scratch dir with umask 0, so its modes come through as
# packed. Linux only: the desktop is packaged for Linux alone.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
target=${1:?usage: desktop-package-check.sh <app dir | .deb | .rpm | .zip> <kith-relay>}
relay=${2:?usage: desktop-package-check.sh <app dir | .deb | .rpm | .zip> <kith-relay>}
bin=kith-desktop

scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
errors=0
bad() {
	echo "desktop-package-check: $(basename "$target"): $*" >&2
	errors=$((errors + 1))
}

# The app's directory, and for the deb, how it installs chrome-sandbox.
case "$target" in
*.deb)
	(umask 0 && dpkg-deb -x "$target" "$scratch")
	app="$scratch/usr/lib/$bin"
	listing=$(dpkg-deb -c "$target")
	grep -Eq "^-rwsr-xr-x root/root .* \./usr/lib/$bin/chrome-sandbox$" <<<"$listing" ||
		bad "chrome-sandbox is not setuid root"
	;;
*.rpm)
	(umask 0 && cd "$scratch" && rpm2cpio "$target" | cpio -idm --quiet)
	app="$scratch/usr/lib/$bin"
	;;
*.zip)
	(umask 0 && unzip -q "$target" -d "$scratch")
	app=$(dirname "$(find "$scratch" -name "$bin" -type f | head -n 1)")
	;;
*)
	app=$target
	;;
esac
if [[ ! -x "$app/$bin" ]]; then
	bad "no $bin executable in the app ($app)"
	exit 1
fi

res="$app/resources"
if ! cmp -s "$relay" "$res/kith-relay"; then
	bad "resources/kith-relay is missing or not $relay"
elif [[ "$(stat -c %a "$res/kith-relay")" != 755 ]]; then
	bad "resources/kith-relay is mode $(stat -c %a "$res/kith-relay"), not 755"
fi

[[ -f "$res/app.asar" ]] || bad "no resources/app.asar"
[[ ! -e "$res/app" ]] || bad "resources/app/ is there beside the ASAR"

fuses=$(NO_COLOR=1 node "$ROOT/desktop/node_modules/@electron/fuses/dist/bin.js" read --app "$app/$bin")
for want in "RunAsNode is Disabled" "EnableNodeOptionsEnvironmentVariable is Disabled" \
	"EnableNodeCliInspectArguments is Disabled" "EnableEmbeddedAsarIntegrityValidation is Enabled" \
	"OnlyLoadAppFromAsar is Enabled"; do
	grep -qx " *$want" <<<"$fuses" || bad "fuse: want \"$want\""
done

writable=$(find "$app" ! -type l -perm /022 -printf '%m %P\n')
[[ -z "$writable" ]] || bad "writable by group or others:"$'\n'"$writable"

if ((errors > 0)); then
	exit 1
fi
echo "desktop-package-check: $(basename "$target"): the relay, the ASAR, the fuses and the modes hold ✓"
