#!/bin/sh
# Writes Toby's runtime units into /run/systemd/system; /run carries over into
# the booted system, so the root filesystem is never modified.

type getarg >/dev/null 2>&1 || . /lib/dracut-lib.sh

ver=$(getarg toby.version=)
[ -n "$ver" ] || exit 0
case "$ver" in
    .* | *[!A-Za-z0-9.+_-]*) exit 0 ;;
esac

units=/run/systemd/system
mkdir -p "$units/multi-user.target.wants"

cat > "$units/run-toby-fs.mount" <<UNIT
[Unit]
Description=Toby file share
DefaultDependencies=no
Before=local-fs.target

[Mount]
What=toby
Where=/run/toby/fs
Type=virtiofs
Options=nosuid,nodev
UNIT

cat > "$units/toby-relay.service" <<UNIT
[Unit]
Description=Toby relay
Requires=run-toby-fs.mount
After=run-toby-fs.mount

[Service]
ExecStart=/run/toby/fs/versions/$ver/toby guest relay
Restart=always
RestartSec=1
UNIT

ln -sf ../toby-relay.service "$units/multi-user.target.wants/toby-relay.service"
