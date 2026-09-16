#!/bin/sh
# Install the latest RCSS release on Linux or macOS.
#
#   curl -fsSL https://raw.githubusercontent.com/dougmb/rcss/main/install.sh | sh
#
# Environment:
#   RCSS_VERSION      release tag to install (default: latest), e.g. v0.2.0
#   RCSS_INSTALL_DIR  where to put the binary (default: ~/.local/bin)
set -eu

REPO="dougmb/rcss"
INSTALL_DIR="${RCSS_INSTALL_DIR:-$HOME/.local/bin}"

say() { printf '%s\n' "$*"; }
die() { printf 'rcss install: %s\n' "$*" >&2; exit 1; }
has() { command -v "$1" >/dev/null 2>&1; }

if has curl; then
	fetch() { curl -fsSL -o "$2" "$1"; }
	latest_url() { curl -fsSLI -o /dev/null -w '%{url_effective}' "$1"; }
elif has wget; then
	fetch() { wget -qO "$2" "$1"; }
	latest_url() { wget -S --spider "$1" 2>&1 | sed -n 's/^ *[Ll]ocation: *//p' | tail -n1 | tr -d '\r'; }
else
	die "curl or wget is required"
fi

case "$(uname -s)" in
Linux) os=linux ;;
Darwin) os=darwin ;;
*) die "unsupported OS $(uname -s); on Windows use install.ps1" ;;
esac

case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*) die "unsupported architecture $(uname -m)" ;;
esac

version="${RCSS_VERSION:-}"
if [ -z "$version" ]; then
	version="$(latest_url "https://github.com/$REPO/releases/latest")"
	version="${version##*/}"
	case "$version" in
	v*) ;;
	*) die "could not determine the latest release (is one published?)" ;;
	esac
fi

archive="rcss_${version#v}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/$version"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

say "Downloading rcss $version ($os/$arch)..."
fetch "$base/$archive" "$tmp/$archive" || die "download failed: $base/$archive"
fetch "$base/checksums.txt" "$tmp/checksums.txt" || die "download failed: $base/checksums.txt"

expected="$(awk -v f="$archive" '$2 == f { print $1 }' "$tmp/checksums.txt")"
[ -n "$expected" ] || die "$archive is not listed in checksums.txt"
if has sha256sum; then
	actual="$(sha256sum "$tmp/$archive" | awk '{ print $1 }')"
elif has shasum; then
	actual="$(shasum -a 256 "$tmp/$archive" | awk '{ print $1 }')"
else
	die "sha256sum or shasum is required to verify the download"
fi
[ "$expected" = "$actual" ] || die "checksum mismatch for $archive"

tar -xzf "$tmp/$archive" -C "$tmp" rcss
mkdir -p "$INSTALL_DIR"
# Replace via rename so a running rcss (e.g. a scheduled job) is not disturbed.
cp "$tmp/rcss" "$INSTALL_DIR/.rcss.new"
chmod 755 "$INSTALL_DIR/.rcss.new"
mv -f "$INSTALL_DIR/.rcss.new" "$INSTALL_DIR/rcss"

say "Installed rcss $version to $INSTALL_DIR/rcss"

case ":$PATH:" in
*":$INSTALL_DIR:"*) ;;
*)
	say ""
	say "$INSTALL_DIR is not on your PATH. Add it to your shell profile:"
	say "  export PATH=\"$INSTALL_DIR:\$PATH\""
	;;
esac

if ! has rclone; then
	say ""
	say "rclone was not found. RCSS needs it for every cloud operation:"
	say "  https://rclone.org/install/"
fi

say ""
say "Run 'rcss' to open the app."
