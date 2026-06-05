#!/bin/sh
set -eu

if [ "$#" -ne 2 ]; then
	printf 'usage: %s BASE_URL ARCH\n' "$0" >&2
	exit 2
fi

base_url=$1
arch=$2

if ! command -v curl >/dev/null 2>&1; then
	printf 'curl is required to install grok\n' >&2
	exit 127
fi

grok_dir="$HOME/.grok"
downloads_dir="$grok_dir/downloads"
bin_dir="$grok_dir/bin"
mkdir -p "$downloads_dir" "$bin_dir"

version="$(curl -fsSL "$base_url/stable")"
if [ -z "$version" ]; then
	printf 'failed to resolve latest Grok version\n' >&2
	exit 1
fi

url="$base_url/grok-${version}-linux-${arch}"
binary="$downloads_dir/grok-linux-${arch}"
tmp_binary="$binary.tmp"

cleanup() {
	rm -f "$tmp_binary"
}
trap cleanup EXIT

curl -fsSL "$url" -o "$tmp_binary"
chmod +x "$tmp_binary"
mv -f "$tmp_binary" "$binary"
ln -sf "../downloads/$(basename "$binary")" "$bin_dir/grok"
ln -sf "../downloads/$(basename "$binary")" "$bin_dir/agent"
