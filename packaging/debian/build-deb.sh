#!/bin/sh
# Builds a Debian package of Toby from this repository.
#
# usage: packaging/debian/build-deb.sh OUTDIR
#
# Environment: as packaging/stage.sh (ARCH, TOBY_BINARY, DOWNLOADS).
# Needs dpkg-deb, plus what stage.sh needs.

set -eu

out=${1:?usage: build-deb.sh OUTDIR}
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
arch=${ARCH:-$(uname -m)}
case $arch in
    x86_64) debarch=amd64 ;;
    aarch64) debarch=arm64 ;;
    *) echo "build-deb.sh: no packages for $arch" >&2; exit 1 ;;
esac
version=$(sed -n 's/^version = "\(.*\)"/\1/p' "$repo/Cargo.toml" | head -n 1)

mkdir -p "$out"
out=$(cd "$out" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
root=$work/root
ARCH=$arch DOWNLOADS=${DOWNLOADS:-$out/downloads} "$repo/packaging/stage.sh" "$root"

# Debian keeps licenses in one copyright file.
doc=$root/usr/share/doc/toby
mkdir -p "$doc"
{
    echo "Toby is under the MIT license. The package also contains the programs"
    echo "and code below, under their own licenses."
    for f in $(cd "$root/usr/share/licenses/toby" && find . -type f | sort); do
        echo
        echo "==> ${f#./}"
        cat "$root/usr/share/licenses/toby/$f"
    done
} > "$doc/copyright"
rm -rf "$root/usr/share/licenses"

mkdir -p "$root/DEBIAN"
cat > "$root/DEBIAN/control" <<CONTROL
Package: toby
Version: $version
Architecture: $debarch
Maintainer: Toby maintainers <toby@localhost>
Depends: passt (>= 0.0~git20250121), systemd
Section: devel
Priority: optional
Description: Run development tools inside virtual machines
 Toby runs coding agents and other development tools in KVM virtual
 machines with the projects they work on shared into them.
CONTROL
for script in postinst prerm postrm; do
    install -m0755 "$here/$script" "$root/DEBIAN/$script"
done
dpkg-deb --build --root-owner-group "$root" "$out/toby_${version}_$debarch.deb"
