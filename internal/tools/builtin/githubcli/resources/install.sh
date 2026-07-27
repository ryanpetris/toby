#!/bin/sh
set -eu

if [ "$#" -ne 1 ]; then
	printf 'usage: %s ARCHIVE_URL\n' "$0" >&2
	exit 2
fi

archive_url=$1
tmp="$(mktemp -d)"

cleanup() {
	rm -rf "$tmp"
}
trap cleanup EXIT

archive="$tmp/gh.tar.gz"
mkdir -p "$HOME/.local/bin"
curl -fsSL "$archive_url" -o "$archive"
tar -xzf "$archive" -C "$tmp"
install -m 0755 "$tmp"/*/bin/gh "$HOME/.local/bin/gh"
