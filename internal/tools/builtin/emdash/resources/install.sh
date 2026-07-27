#!/bin/sh
set -eu

if [ "$#" -ne 1 ]; then
	printf 'usage: %s APPIMAGE_URL\n' "$0" >&2
	exit 2
fi

appimage_url=$1

if ! command -v curl >/dev/null 2>&1; then
	printf 'curl is required to install emdash\n' >&2
	exit 127
fi

apps_dir="$HOME/.local/apps"
appimage="$apps_dir/emdash.AppImage"
tmp_appimage="$appimage.tmp"
bin_dir="$HOME/.local/bin"
launcher="$bin_dir/emdash"

mkdir -p "$apps_dir" "$bin_dir"
rm -rf "$apps_dir/emdash" "$tmp_appimage"

cleanup() {
	rm -f "$tmp_appimage"
}
trap cleanup EXIT

curl -fsSL "$appimage_url" -o "$tmp_appimage"
chmod +x "$tmp_appimage"
mv -f "$tmp_appimage" "$appimage"
printf '%s\n' '#!/bin/sh' 'APPIMAGE_EXTRACT_AND_RUN=1 exec "$HOME/.local/apps/emdash.AppImage" "$@"' > "$launcher"
chmod +x "$launcher"
