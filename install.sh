#!/bin/sh
# Partitur installer — fetches a released archive and puts all four binaries on disk.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/BeomSeogKim/Partitur/main/install.sh | sh
#
# Environment:
#   PARTITUR_VERSION      tag to install (e.g. v0.2.0); default: the latest release
#   PARTITUR_INSTALL_DIR  destination directory; default: $HOME/.local/bin
#
# No privilege escalation: this script never calls sudo. If the destination
# needs root, install somewhere you own and put that directory on PATH.

set -eu

REPO="BeomSeogKim/Partitur"
INSTALL_DIR="${PARTITUR_INSTALL_DIR:-$HOME/.local/bin}"
BINARIES="partitur partitur-adapter-codex partitur-adapter-claude partitur-trampoline"

die() {
	echo "install.sh: $*" >&2
	exit 1
}

need() {
	command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

need uname
need mkdir
need tar
need mktemp

if command -v curl >/dev/null 2>&1; then
	downloader=curl
elif command -v wget >/dev/null 2>&1; then
	downloader=wget
else
	die "neither curl nor wget is available; one of them is required"
fi

fetch() {
	# fetch <url> <destination>
	case "$downloader" in
	curl) curl -fsSL -o "$2" "$1" || die "download failed: $1" ;;
	wget) wget -qO "$2" "$1" || die "download failed: $1" ;;
	esac
}

os=$(uname -s)
case "$os" in
Darwin) os=darwin ;;
Linux) os=linux ;;
*) die "unsupported operating system: $os (darwin and linux are released)" ;;
esac

arch=$(uname -m)
case "$arch" in
arm64 | aarch64) arch=arm64 ;;
x86_64 | amd64) arch=amd64 ;;
*) die "unsupported architecture: $arch (arm64 and amd64 are released)" ;;
esac

tag="${PARTITUR_VERSION:-}"
if [ -z "$tag" ]; then
	api="https://api.github.com/repos/$REPO/releases/latest"
	latest_json=$(mktemp) || die "cannot create a temporary file"
	fetch "$api" "$latest_json"
	# The tag_name line, without a JSON parser: "tag_name": "v0.2.0",
	tag=$(sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$latest_json" | head -n 1)
	rm -f "$latest_json"
	[ -n "$tag" ] || die "could not resolve the latest release tag from $api; set PARTITUR_VERSION=vX.Y.Z to pin one"
fi

# Archive names carry the version without the leading "v".
version=${tag#v}
archive="partitur_${version}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/$tag"

tmp=$(mktemp -d) || die "cannot create a temporary directory"
cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT INT TERM

echo "Installing Partitur $tag ($os/$arch) into $INSTALL_DIR"

fetch "$base/$archive" "$tmp/$archive"
fetch "$base/checksums.txt" "$tmp/checksums.txt"

expected=$(awk -v file="$archive" '$2 == file || $2 == "*" file { print $1 }' "$tmp/checksums.txt" | head -n 1)
[ -n "$expected" ] || die "$archive is not listed in checksums.txt for $tag"

if command -v shasum >/dev/null 2>&1; then
	actual=$(shasum -a 256 "$tmp/$archive" | awk '{ print $1 }')
elif command -v sha256sum >/dev/null 2>&1; then
	actual=$(sha256sum "$tmp/$archive" | awk '{ print $1 }')
else
	die "neither shasum nor sha256sum is available; cannot verify the download"
fi

[ "$actual" = "$expected" ] || die "checksum mismatch for $archive: expected $expected, got $actual"
echo "Checksum verified (sha256 $actual)"

tar -xzf "$tmp/$archive" -C "$tmp" || die "could not extract $archive"

for binary in $BINARIES; do
	[ -f "$tmp/$binary" ] || die "$archive does not contain $binary"
done

mkdir -p "$INSTALL_DIR" || die "cannot create $INSTALL_DIR"
[ -w "$INSTALL_DIR" ] || die "$INSTALL_DIR is not writable; set PARTITUR_INSTALL_DIR to a directory you own"

for binary in $BINARIES; do
	cp "$tmp/$binary" "$INSTALL_DIR/$binary.new" || die "cannot write $INSTALL_DIR/$binary"
	chmod 755 "$INSTALL_DIR/$binary.new" || die "cannot make $INSTALL_DIR/$binary executable"
	# Replace in one step so a running binary is never truncated mid-install.
	mv -f "$INSTALL_DIR/$binary.new" "$INSTALL_DIR/$binary" || die "cannot install $INSTALL_DIR/$binary"
	echo "  installed $INSTALL_DIR/$binary"
done

# All four must resolve from PATH: the core looks up its siblings by name.
case ":$PATH:" in
*":$INSTALL_DIR:"*) ;;
*)
	echo
	echo "$INSTALL_DIR is not on your PATH. Add this line to your shell profile:"
	echo
	echo "  export PATH=\"$INSTALL_DIR:\$PATH\""
	;;
esac
