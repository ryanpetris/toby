#!/bin/bash

status_fd=
block_fd=
arguments_fd=

while [ "$#" -gt 0 ]; do
	case "$1" in
	--json-status-fd)
		status_fd=$2
		shift 2
		;;
	--block-fd)
		block_fd=$2
		shift 2
		;;
	--args)
		arguments_fd=$2
		shift 2
		;;
	*)
		exit 2
		;;
	esac
done

case "$status_fd:$block_fd:$arguments_fd" in
*[!0-9:]* | :* | *: | *::*)
	exit 2
	;;
esac

/bin/sh -c '
	status_fd=$1
	IFS= read -r _ || :
	eval "exec ${status_fd}>&-"
	sleep 0.05
' toby-fake-confidential "$status_fd" <&"$block_fd" &
child_pid=$!
mnt_namespace=$(stat -Lc %i "/proc/${child_pid}/ns/mnt") || exit 2
eval "printf '%s\n' '{ \"child-pid\": ${child_pid}, \"mnt-namespace\": ${mnt_namespace} }' >&${status_fd}"
eval "exec ${block_fd}>&-"
wait "$child_pid"

while IFS= read -r -d '' argument; do
	printf 'argv:%s\n' "$argument"
done <"/proc/$$/cmdline"

while IFS= read -r -d '' -u "$arguments_fd" argument; do
	printf 'payload:%s\n' "$argument"
done

eval "printf '%s\n' '{ \"exit-code\": 0 }' >&${status_fd}"
