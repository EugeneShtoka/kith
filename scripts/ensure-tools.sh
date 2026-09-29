#!/usr/bin/env bash
# ensure-tools: install the tools CI and `make check` both run, at the versions in
# tools/versions.env, into .tools/ (gitignored). Each archive is checked against its
# pinned SHA-256 before anything is unpacked, and a tool is installed only when its
# pinned version is not already there, so the call is cheap enough to run before
# every gate.
#
#   scripts/ensure-tools.sh [goreleaser|gitleaks|buf|markdownlint]...   (default: all)
#
# For the tests (scripts/test/tools-test.sh): TOOLS_DIR overrides .tools,
# VERSIONS_FILE overrides tools/versions.env, and TOOLS_BASE_URL replaces
# https://github.com for the downloads (file:// URLs serve local archives).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSIONS_FILE="${VERSIONS_FILE:-$ROOT/tools/versions.env}"
# shellcheck source=../tools/versions.env
. "$VERSIONS_FILE"
TOOLS_DIR="${TOOLS_DIR:-$ROOT/.tools}"
BASE="${TOOLS_BASE_URL:-https://github.com}"
BIN="$TOOLS_DIR/bin"

case "$(uname -s)" in
Linux) os=linux ;;
Darwin) os=darwin ;;
*) echo "ensure-tools: no pinned binaries for $(uname -s); install goreleaser, gitleaks and buf by hand" >&2; exit 1 ;;
esac
case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*) echo "ensure-tools: no pinned binaries for $(uname -m)" >&2; exit 1 ;;
esac

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi
}

# install <name> <version> <sha256> <url> <path of the binary inside the archive>
install() {
	local name=$1 version=$2 want=$3 url=$4 member=$5
	local stamp="$TOOLS_DIR/stamps/$name"
	if [[ -x "$BIN/$name" && -f "$stamp" && "$(cat "$stamp")" == "$version" ]]; then
		return 0
	fi
	if [[ -z "$want" ]]; then
		echo "ensure-tools: tools/versions.env has no $name checksum for ${os}_${arch}" >&2
		return 1
	fi
	mkdir -p "$BIN" "$TOOLS_DIR/stamps"
	local tmp
	tmp=$(mktemp -d)
	# shellcheck disable=SC2064 # expand now: tmp is local
	trap "rm -rf '$tmp'" RETURN
	echo "ensure-tools: installing $name $version" >&2
	if ! curl -sSfL --retry 3 "$url" -o "$tmp/archive.tar.gz"; then
		echo "ensure-tools: could not download $url" >&2
		return 1
	fi
	local got
	got=$(sha256 "$tmp/archive.tar.gz")
	if [[ "$got" != "$want" ]]; then
		echo "ensure-tools: $name $version checksum mismatch: got $got, want $want (nothing installed)" >&2
		return 1
	fi
	tar -xzf "$tmp/archive.tar.gz" -C "$tmp" "$member"
	# Replace atomically, so a failed run never leaves a half-written binary.
	install_bin "$tmp/$member" "$BIN/$name"
	echo "$version" >"$stamp"
}

install_bin() {
	chmod 0755 "$1"
	mv -f "$1" "$2.new"
	mv -f "$2.new" "$2"
}

want_sum() { local var="${1}_SHA256_${os}_${arch}"; echo "${!var:-}"; }

goreleaser() {
	local asset
	case "$os" in
	linux) asset="goreleaser_Linux_$([[ $arch == amd64 ]] && echo x86_64 || echo arm64).tar.gz" ;;
	darwin) asset=goreleaser_Darwin_all.tar.gz ;;
	esac
	install goreleaser "$GORELEASER_VERSION" "$(want_sum GORELEASER)" \
		"$BASE/goreleaser/goreleaser/releases/download/v$GORELEASER_VERSION/$asset" goreleaser
}

gitleaks() {
	local a
	a=$([[ $arch == amd64 ]] && echo x64 || echo arm64)
	install gitleaks "$GITLEAKS_VERSION" "$(want_sum GITLEAKS)" \
		"$BASE/gitleaks/gitleaks/releases/download/v$GITLEAKS_VERSION/gitleaks_${GITLEAKS_VERSION}_${os}_$a.tar.gz" gitleaks
}

buf() {
	local o a
	o=$([[ $os == linux ]] && echo Linux || echo Darwin)
	case "$os/$arch" in
	linux/amd64 | darwin/amd64) a=x86_64 ;;
	linux/arm64) a=aarch64 ;;
	darwin/arm64) a=arm64 ;;
	esac
	install buf "$BUF_VERSION" "$(want_sum BUF)" \
		"$BASE/bufbuild/buf/releases/download/v$BUF_VERSION/buf-$o-$a.tar.gz" buf/bin/buf
}

# markdownlint-cli2 and its whole dependency tree, exactly as the lock says.
markdownlint() {
	local src="$ROOT/tools/markdownlint" dest="$TOOLS_DIR/markdownlint"
	local lock
	lock=$(sha256 "$src/package-lock.json")
	if [[ -x "$dest/node_modules/.bin/markdownlint-cli2" && -f "$TOOLS_DIR/stamps/markdownlint" &&
		"$(cat "$TOOLS_DIR/stamps/markdownlint")" == "$lock" ]]; then
		return 0
	fi
	if ! command -v npm >/dev/null 2>&1 || ! npm --version >/dev/null 2>&1; then
		echo "ensure-tools: markdownlint needs node and npm (https://nodejs.org)" >&2
		return 1
	fi
	echo "ensure-tools: installing markdownlint-cli2 from the lock" >&2
	mkdir -p "$dest" "$TOOLS_DIR/stamps" "$BIN"
	cp "$src/package.json" "$src/package-lock.json" "$dest/"
	(cd "$dest" && npm ci --ignore-scripts --no-audit --no-fund --silent)
	ln -sf "../markdownlint/node_modules/.bin/markdownlint-cli2" "$BIN/markdownlint-cli2"
	echo "$lock" >"$TOOLS_DIR/stamps/markdownlint"
}

tools=("$@")
[[ ${#tools[@]} -gt 0 ]] || tools=(goreleaser gitleaks buf markdownlint)
for t in "${tools[@]}"; do
	case "$t" in
	goreleaser | gitleaks | buf | markdownlint) "$t" ;;
	*) echo "ensure-tools: unknown tool $t" >&2; exit 2 ;;
	esac
done
