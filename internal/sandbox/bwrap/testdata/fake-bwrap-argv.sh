#!/bin/bash

if [ "$1" != "--json-status-fd" ]; then
	exit 2
fi
status_fd=$2

case "$status_fd" in
"" | *[!0-9]*)
	exit 2
	;;
esac

if [ "$3" != "--block-fd" ]; then
	exit 2
fi
block_fd=$4

case "$block_fd" in
"" | *[!0-9]*)
	exit 2
	;;
esac

/bin/sh -c '
	status_fd=$1
	IFS= read -r _ || :
	eval "exec ${status_fd}>&-"
	sleep 0.05
' toby-fake-argv "$status_fd" <&"$block_fd" &
child_pid=$!
mnt_namespace=$(stat -Lc %i "/proc/${child_pid}/ns/mnt") || exit 2
eval "printf '%s\n' '{ \"child-pid\": ${child_pid}, \"mnt-namespace\": ${mnt_namespace} }' >&${status_fd}"
eval "exec ${block_fd}>&-"
wait "$child_pid"
printf '%s\n' "$@"
eval "printf '%s\n' '{ \"exit-code\": 0 }' >&${status_fd}"
