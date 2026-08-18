#!/bin/sh
set -eu

if [ "$#" -ne 1 ]; then
	printf 'usage: %s ARCH\n' "$0" >&2
	exit 2
fi

arch=$1
install_url=https://cursor.com/install
downloads_base=https://downloads.cursor.com/lab

if ! command -v curl >/dev/null 2>&1; then
	printf 'curl is required to install cursor\n' >&2
	exit 127
fi

if ! command -v tar >/dev/null 2>&1; then
	printf 'tar is required to install cursor\n' >&2
	exit 127
fi

installer="$(curl -fsSL "$install_url")"
version="$(printf '%s\n' "$installer" | sed -n 's|.*downloads.cursor.com/lab/\([^/]*\)/.*|\1|p' | head -n 1)"
if [ -z "$version" ]; then
	printf 'failed to resolve latest Cursor CLI version\n' >&2
	exit 1
fi

url="$downloads_base/$version/linux/$arch/agent-cli-package.tar.gz"
share_dir="$HOME/.local/share/cursor-agent/versions"
final_dir="$share_dir/$version"
bin_dir="$HOME/.local/bin"
tmp="$(mktemp -d)"
staging=

cleanup() {
	rm -rf "$tmp"
	if [ -n "$staging" ]; then
		rm -rf "$staging"
	fi
}
trap cleanup EXIT

mkdir -p "$share_dir" "$bin_dir"

# A shared tool volume may already hold this version from another private home.
if [ -x "$final_dir/cursor-agent" ]; then
	ln -sf "$final_dir/cursor-agent" "$bin_dir/cursor-agent"
	exit 0
fi

archive="$tmp/agent-cli-package.tar.gz"
curl -fsSL "$url" -o "$archive"

extract_dir="$tmp/extract"
mkdir -p "$extract_dir"
tar -xzf "$archive" -C "$extract_dir"

binary="$(find "$extract_dir" -type f -name cursor-agent | head -n 1)"
if [ -z "$binary" ]; then
	printf 'Cursor CLI archive does not contain cursor-agent\n' >&2
	exit 1
fi

package_dir="$(dirname "$binary")"
staging="$share_dir/.tmp-$version-$$"
rm -rf "$staging"
mkdir -p "$staging"
tar -C "$package_dir" -cf - . | tar -C "$staging" -xf -
chmod +x "$staging/cursor-agent"
rm -rf "$final_dir"
mv "$staging" "$final_dir"
staging=

# Install only cursor-agent. The official agent alias would collide with
# Grok's agent command when both tools share a sandbox.
ln -sf "$final_dir/cursor-agent" "$bin_dir/cursor-agent"
