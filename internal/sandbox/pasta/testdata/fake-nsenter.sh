#!/bin/sh

while [ "$#" -gt 0 ]; do
	if [ "$1" = "--" ]; then
		shift
		exec "$@"
	fi
	shift
done

exit 2
