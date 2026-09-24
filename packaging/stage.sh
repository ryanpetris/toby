#!/bin/sh
# Lays out Toby's package contents under DESTDIR (plan §3.3, §4): the
# binary, the programs bundled from packaging/bundled.toml with their
# licenses, the files served to builder machines, and the systemd units.
#
# usage: packaging/stage.sh DESTDIR
#
# Environment:
#   ARCH          x86_64 or aarch64 (default: this machine's)
#   TOBY_BINARY   a static toby binary to use instead of building one
#                 (building needs a musl C compiler: musl-gcc)
#   DOWNLOADS     where downloads are kept between runs (default: DESTDIR/../downloads)
#
# Needs curl, jq, sha256sum, tar and xz. Release assets are checked against the
# SHA-256 digests GitHub publishes for them; source archives have none, so
# their digests are printed.

set -eu

dest=${1:?usage: stage.sh DESTDIR}
repo=$(cd "$(dirname "$0")/.." && pwd)
arch=${ARCH:-$(uname -m)}
case $arch in
    x86_64|aarch64) ;;
    *) echo "stage.sh: no packages for $arch" >&2; exit 1 ;;
esac
mkdir -p "$dest"
dest=$(cd "$dest" && pwd)
downloads=${DOWNLOADS:-$dest/../downloads}
mkdir -p "$downloads"
downloads=$(cd "$downloads" && pwd)

# The value of KEY in [SECTION] of bundled.toml (simple `key = "value"` and
# inline tables of strings).
bundled() {
    awk -v section="[$1]" -v key="$2" '
        /^\[/ { inside = ($0 == section); next }
        inside && $1 == key { sub(/^[^=]*= */, ""); print; exit }
    ' "$repo/packaging/bundled.toml"
}
unquote() { sed -e 's/^"//' -e 's/"$//'; }
# The ARCH entry of an inline table such as { x86_64 = "a", aarch64 = "b" }.
for_arch() { sed -n "s/.*$arch *= *\"\\([^\"]*\\)\".*/\\1/p"; }

# Downloads a GitHub release asset and checks it against its digest.
release_asset() { # repo tag asset out
    json=$downloads/$(echo "$1" | tr / _)-$2.json
    [ -s "$json" ] || curl -fsSL "https://api.github.com/repos/$1/releases/tags/$2" -o "$json"
    want=$(jq -r --arg a "$3" '.assets[] | select(.name == $a) | .digest // empty' "$json" | sed 's/^sha256://')
    [ -n "$want" ] || { echo "stage.sh: $1 $2 publishes no digest for $3" >&2; exit 1; }
    file=$downloads/$2-$3
    [ -s "$file" ] || curl -fsSL "https://github.com/$1/releases/download/$2/$3" -o "$file"
    have=$(sha256sum "$file" | cut -d' ' -f1)
    if [ "$have" != "$want" ]; then
        rm -f "$file"
        echo "stage.sh: $3 of $1 $2 does not match its published digest" >&2
        exit 1
    fi
    install -Dm0644 "$file" "$4"
}

# Downloads a source archive, which has no published digest.
source_archive() { # url out
    [ -s "$2" ] || curl -fsSL "$1" -o "$2"
    echo "==> $(basename "$2"): sha256 $(sha256sum "$2" | cut -d' ' -f1)"
}

version=$(sed -n 's/^version = "\(.*\)"/\1/p' "$repo/Cargo.toml" | head -n 1)
lib=$dest/usr/lib/toby
share=$dest/usr/share/toby
licenses=$dest/usr/share/licenses/toby

echo "==> Toby $version for $arch"
if [ -n "${TOBY_BINARY:-}" ]; then
    binary=$TOBY_BINARY
else
    (cd "$repo" && cargo build --release --locked --target "$arch-unknown-linux-musl" -p toby)
    binary=$repo/target/$arch-unknown-linux-musl/release/toby
fi
# The package's copy; installing it makes it versions/<version>/toby.
install -Dm0755 "$binary" "$lib/dist/toby"
mkdir -p "$dest/usr/bin"
ln -sfn ../lib/toby/versions/current/toby "$dest/usr/bin/toby"
install -Dm0644 "$repo/LICENSE" "$licenses/LICENSE"

echo "==> Cloud Hypervisor"
tag=$(bundled cloud-hypervisor tag | unquote)
asset=$(bundled cloud-hypervisor assets | for_arch)
release_asset cloud-hypervisor/cloud-hypervisor "$tag" "$asset" "$lib/cloud-hypervisor"
chmod 0755 "$lib/cloud-hypervisor"
src=$downloads/cloud-hypervisor-$tag.tar.xz
release_asset cloud-hypervisor/cloud-hypervisor "$tag" "cloud-hypervisor-$tag.tar.xz" "$src.checked"
mkdir -p "$licenses/cloud-hypervisor"
tar -xJf "$src.checked" -C "$licenses/cloud-hypervisor" --strip-components=1 --wildcards '*/LICENSE*'
rm -f "$src.checked"

echo "==> Firmware"
tag=$(bundled edk2 tag | unquote)
asset=$(bundled edk2 assets | for_arch)
release_asset cloud-hypervisor/edk2 "$tag" "$asset" "$lib/firmware/$asset"
source_archive "https://raw.githubusercontent.com/cloud-hypervisor/edk2/$tag/License.txt" "$downloads/edk2-$tag-License.txt"
install -Dm0644 "$downloads/edk2-$tag-License.txt" "$licenses/edk2/License.txt"

echo "==> mkosi"
tag=$(bundled mkosi tag | unquote)
source_archive "https://github.com/systemd/mkosi/archive/refs/tags/$tag.tar.gz" "$downloads/mkosi-$tag.tar.gz"
rm -rf "$share/mkosi"
mkdir -p "$share/mkosi"
tar -xzf "$downloads/mkosi-$tag.tar.gz" -C "$share/mkosi" --strip-components=1
mkdir -p "$licenses/mkosi"
cp -r "$share/mkosi/LICENSES/." "$licenses/mkosi/"

echo "==> Builder files and units"
mkdir -p "$share/images" "$share/dracut"
cp -r "$repo/packaging/images/default" "$share/images/"
cp -r "$repo/packaging/dracut/99toby" "$share/dracut/"
for unit in "$repo"/packaging/systemd/user/*; do
    install -Dm0644 "$unit" "$dest/usr/lib/systemd/user/$(basename "$unit")"
done
for unit in "$repo"/packaging/systemd/system/*; do
    install -Dm0644 "$unit" "$dest/usr/lib/systemd/system/$(basename "$unit")"
done
echo "==> Staged in $dest"
