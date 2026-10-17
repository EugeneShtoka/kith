#!/usr/bin/env bash
# build-libsignal: build libsignal_ffi, the Rust library kith-signal links through
# mautrix-signal's libsignalgo (cgo), at exactly the version that libsignalgo wraps:
# go.mod's mautrix-signal names it (pkg/libsignalgo/signalversion), so bumping the
# module moves libsignal with it. Prints the directory holding libsignal_ffi.a.
#
#   scripts/build-libsignal.sh
#
# Needs git, cargo (rustup), cmake, clang and protoc. LIBSIGNAL_DIR overrides where the
# source is checked out and built (default .tools/libsignal-<version>); a build of the
# right version there is reused.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

module=$(go list -m -f '{{.Dir}}' go.mau.fi/mautrix-signal)
version=$(sed -n 's/^const Version = "\(v[0-9.]*\)"$/\1/p' "$module/pkg/libsignalgo/signalversion/version.go")
if [[ -z $version ]]; then
	echo "build-libsignal: no libsignal version in $module/pkg/libsignalgo/signalversion" >&2
	exit 1
fi

dir="${LIBSIGNAL_DIR:-$ROOT/.tools/libsignal-$version}"
lib="$dir/target/release/libsignal_ffi.a"
if [[ -d $dir/.git ]]; then
	have=$(git -C "$dir" describe --tags --exact-match 2>/dev/null || true)
	if [[ $have != "$version" ]]; then
		echo "build-libsignal: $dir holds libsignal ${have:-(no tag)}, not $version" >&2
		exit 1
	fi
else
	git clone --quiet --depth 1 --branch "$version" https://github.com/signalapp/libsignal "$dir" >&2
fi

if [[ ! -f $lib ]]; then
	# As mautrix-signal's own build-rust.sh: a library linked dynamically against libc.
	(cd "$dir" && RUSTFLAGS="-Ctarget-feature=-crt-static" RUSTC_WRAPPER="" \
		cargo build --quiet -p libsignal-ffi --profile=release) >&2
fi
echo "$dir/target/release"
