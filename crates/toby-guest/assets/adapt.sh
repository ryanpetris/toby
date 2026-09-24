#!/bin/sh
# Boot adaptation: makes a root filesystem tree boot as a Toby machine.
# Runs as root in a builder machine, in a private mount namespace so the mounts
# it needs never leak into the exported image.
#
# usage: adapt.sh TREE
# Environment: TOBY_ADAPTATION_VERSION (set by Toby) is recorded in the image;
# TOBY_ADAPT_MANUAL=1 skips package installation.
# Writes the kernel version to /run/toby/build/kernel-version.

set -eu
if [ -z "${TOBY_ADAPT_NS:-}" ]; then
    exec env TOBY_ADAPT_NS=1 unshare -m --propagation private sh "$0" "$@"
fi

tree=$1
adaptation_version=${TOBY_ADAPTATION_VERSION:?}
drivers="virtio_pci virtio_blk virtio_net virtio_console virtiofs vmw_vsock_virtio_transport ext4"

mount --bind /proc "$tree/proc"
mount --rbind /sys "$tree/sys"
mount --rbind /dev "$tree/dev"
mkdir -p "$tree/run" "$tree/tmp"
mount -t tmpfs tmpfs "$tree/run"
mount -t tmpfs tmpfs "$tree/tmp"

# Package managers in the tree resolve names through the builder's resolver.
resolv="$tree/etc/resolv.conf"
restore_resolv=
if [ -L "$resolv" ] || [ -e "$resolv" ]; then
    mv "$resolv" "$resolv.toby-saved"
    restore_resolv=1
fi
cp /etc/resolv.conf "$resolv"
finish() {
    rm -f "$resolv"
    if [ -n "$restore_resolv" ]; then
        mv "$resolv.toby-saved" "$resolv"
    fi
}
trap finish EXIT

in_tree() {
    chroot "$tree" /usr/bin/env PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin "$@"
}
have() {
    in_tree sh -c "command -v $1" >/dev/null 2>&1
}

# The newest kernel version that has a kernel image (headers alone create a
# modules directory too).
newest_kernel() {
    for kver in $(ls "$tree/usr/lib/modules" 2>/dev/null | sort -rV); do
        if [ -e "$tree/usr/lib/modules/$kver/vmlinuz" ] || [ -e "$tree/boot/vmlinuz-$kver" ]; then
            echo "$kver"
            return
        fi
    done
}

family=
if [ -r "$tree/etc/os-release" ] || [ -r "$tree/usr/lib/os-release" ]; then
    os_release="$tree/etc/os-release"
    [ -r "$os_release" ] || os_release="$tree/usr/lib/os-release"
    # Read, never sourced: the file comes from the image.
    ids=$(sed -n 's/^\(ID\|ID_LIKE\)=//p' "$os_release" | tr -d "\"'" | tr -c 'a-z0-9._\n-' ' ')
    for id in $ids; do
        case $id in
            debian | ubuntu) family=apt ;;
            fedora | rhel | centos) family=dnf ;;
            arch) family=pacman ;;
            suse | opensuse*) family=zypper ;;
        esac
        [ -n "$family" ] && break
    done
fi

missing=
[ -x "$tree/usr/lib/systemd/systemd" ] || [ -x "$tree/lib/systemd/systemd" ] || missing="$missing systemd"
have sudo || missing="$missing sudo"
have dracut || missing="$missing dracut"
[ -n "$(newest_kernel)" ] || missing="$missing kernel"

if [ -n "$missing" ]; then
    if [ -n "${TOBY_ADAPT_MANUAL:-}" ]; then
        echo "The image lacks:$missing (it is marked dev.toby.adapted=manual)" >&2
        exit 1
    fi
    echo "==> Installing:$missing"
    case $family in
        apt)
            case $(uname -m) in aarch64) debarch=arm64 ;; *) debarch=amd64 ;; esac
            case " $ids " in
                *" ubuntu "*) kernel=linux-virtual ;;
                *) kernel="linux-image-cloud-$debarch" ;;
            esac
            pkgs="systemd systemd-sysv sudo dracut ca-certificates"
            case $missing in *kernel*) pkgs="$pkgs $kernel" ;; esac
            in_tree sh -c "export DEBIAN_FRONTEND=noninteractive; apt-get update -q && apt-get install -y -q --no-install-recommends $pkgs"
            ;;
        dnf)
            pkgs="systemd sudo dracut ca-certificates"
            case $missing in *kernel*) pkgs="$pkgs kernel-core" ;; esac
            in_tree dnf install -y $pkgs
            ;;
        pacman)
            # Arch supports only full upgrades.
            pkgs="systemd sudo dracut ca-certificates"
            case $missing in *kernel*) pkgs="$pkgs linux" ;; esac
            in_tree pacman -Syu --noconfirm --needed $pkgs
            ;;
        zypper)
            pkgs="systemd sudo dracut ca-certificates"
            case $missing in *kernel*) pkgs="$pkgs kernel-default-base" ;; esac
            in_tree zypper -n install $pkgs
            ;;
        *)
            echo "Unsupported distribution: Toby can adapt Debian, Ubuntu, Fedora, RHEL, Arch and openSUSE" >&2
            echo "images; other images must provide systemd, a kernel and dracut and carry the label" >&2
            echo "dev.toby.adapted=manual." >&2
            exit 1
            ;;
    esac
fi

kver=$(newest_kernel)
[ -n "$kver" ] || { echo "No kernel found in the image" >&2; exit 1; }

if have systemd-hwdb; then
    in_tree systemd-hwdb update || true
fi

mkdir -p "$tree/usr/lib/dracut/modules.d/99toby"
cp /run/toby/fs/dracut/99toby/* "$tree/usr/lib/dracut/modules.d/99toby/"
mkdir -p "$tree/boot"
echo "==> Generating the initramfs for $kver"
in_tree dracut --quiet --no-hostonly --force --kver "$kver" --add toby --add-drivers "$drivers" /boot/toby-initramfs.img

# systemd is init, whatever the image had there.
case $(in_tree readlink -f /sbin/init 2>/dev/null) in
    */systemd) ;;
    *)
        if [ -x "$tree/usr/lib/systemd/systemd" ]; then systemd=/usr/lib/systemd/systemd; else systemd=/lib/systemd/systemd; fi
        in_tree ln -sfn "$systemd" /sbin/init
        ;;
esac
# Toby configures the network itself (net-up); anything that would manage the
# interface or resolver in the guest is masked, along with console logins and
# first-boot setup.
in_tree systemctl mask --quiet getty@tty1.service serial-getty@hvc0.service systemd-firstboot.service \
    systemd-networkd.service systemd-networkd.socket systemd-networkd-wait-online.service \
    NetworkManager.service NetworkManager-wait-online.service networking.service dhcpcd.service \
    connman.service wicked.service || true
: > "$tree/etc/machine-id"
echo "$adaptation_version" > "$tree/usr/lib/toby-adaptation"

mkdir -p /run/toby/build
echo "$kver" > /run/toby/build/kernel-version
