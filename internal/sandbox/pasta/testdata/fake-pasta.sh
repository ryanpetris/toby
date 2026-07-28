#!/bin/sh

pid_file=
while [ "$#" -gt 0 ]; do
	case "$1" in
		--pid)
			pid_file=$2
			shift 2
			;;
		*)
			shift
			;;
	esac
done

printf '%s\n' "$$" > "$pid_file"
trap 'exit 0' TERM INT
while :; do
	/bin/sleep 0.05
done
