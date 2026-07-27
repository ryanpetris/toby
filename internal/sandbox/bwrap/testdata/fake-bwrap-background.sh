#!/bin/bash

if [ "$1" != "--json-status-fd" ]; then
	exit 2
fi
status_fd=$2
shift 2

if [ "$1" = "--args" ]; then
	arguments_fd=$2
	shift 2
	arguments=()
	while IFS= read -r -d '' -u "$arguments_fd" argument; do
		arguments+=("$argument")
	done
	set -- "${arguments[@]}"
fi

while [ "$#" -gt 0 ] && [ "$1" != "--" ]; do
	shift
done
if [ "$#" -eq 0 ]; then
	exit 2
fi
shift

exec 200<&0

(
	eval "exec ${status_fd}>&-"
	"$@" <&200 &
	payload_pid=$!
	wait "$payload_pid"
) <&0 &
init_pid=$!

mnt_namespace=$(stat -Lc %i "/proc/$$/ns/mnt") || exit 2
eval "printf '%s\n' '{ \"child-pid\": ${init_pid}, \"mnt-namespace\": ${mnt_namespace} }' >&${status_fd}"
wait "$init_pid"
code=$?
eval "printf '%s\n' '{ \"exit-code\": ${code} }' >&${status_fd}"
exit "$code"
