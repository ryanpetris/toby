#!/bin/sh
set -u

child=

terminate_child() {
	if [ -n "$child" ]; then
		kill -TERM "$child" 2>/dev/null || true
	fi
}

forward_child() {
	if [ -n "$child" ]; then
		kill "-$1" "$child" 2>/dev/null || true
	fi
}

trap terminate_child INT
trap 'forward_child TERM' TERM
trap 'forward_child HUP' HUP
trap 'forward_child QUIT' QUIT

t3 "$@" &
child=$!

while true; do
	wait "$child"
	status=$?
	if ! kill -0 "$child" 2>/dev/null; then
		exit "$status"
	fi
done
