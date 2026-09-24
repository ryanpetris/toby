#!/bin/sh
# Writes Toby's runtime units into /run/systemd/system; /run carries over into
# the booted system, so the root filesystem is never modified. dracut sources
# this hook, so it must not exit.

type getarg >/dev/null 2>&1 || . /lib/dracut-lib.sh

toby_version=$(getarg toby.version=)
case "$toby_version" in
    "" | .* | *[!A-Za-z0-9.+_-]*) ;;
    *)
        toby_units=/run/systemd/system
        mkdir -p "$toby_units/multi-user.target.wants"
        cp /usr/lib/toby/run-toby-fs.mount "$toby_units/run-toby-fs.mount"
        sed "s/@VERSION@/$toby_version/" /usr/lib/toby/toby-relay.service.in > "$toby_units/toby-relay.service"
        ln -sf ../toby-relay.service "$toby_units/multi-user.target.wants/toby-relay.service"
        unset toby_units
        ;;
esac
unset toby_version
