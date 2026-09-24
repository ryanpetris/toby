#!/bin/bash
# dracut module that makes an image boot as a Toby machine.

check() {
    return 0
}

depends() {
    return 0
}

installkernel() {
    hostonly='' instmods virtio_pci virtio_blk virtio_net virtio_console virtiofs vmw_vsock_virtio_transport ext4
}

install() {
    inst_simple "$moddir/run-toby-fs.mount" /usr/lib/toby/run-toby-fs.mount
    inst_simple "$moddir/toby-relay.service.in" /usr/lib/toby/toby-relay.service.in
    inst_hook pre-pivot 90 "$moddir/toby-units.sh"
}
