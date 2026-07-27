#!/bin/bash

if [ "$1" != "--json-status-fd" ]; then
	exit 2
fi
status_fd=$2
shift 2

case "$status_fd" in
"" | *[!0-9]*)
	exit 2
	;;
esac

if [ "$1" != "--block-fd" ]; then
	exit 2
fi
block_fd=$2
shift 2

case "$block_fd" in
"" | *[!0-9]*)
	exit 2
	;;
esac

exec 200<&0

payload_helper=
if [ "$1" = "--fake-payload-helper" ]; then
	payload_helper=$2
	shift 2
fi

run_shell_child() {
	child_status_fd=$1
	shift

	IFS= read -r _ || :
	eval "exec ${child_status_fd}>&-"
	exec 0<&200
	if [ "$1" = "/toby/bin/tobys" ] &&
		[ "$2" = "exec" ] &&
		[ "$6" = "--" ]; then
		ready_fd=$3
		stderr_fd=$4
		signal_fd=$5
		if [ -n "$payload_helper" ]; then
			trap - INT TERM HUP QUIT
			export TOBY_SANDBOX=1
			exec "$payload_helper" \
				exec \
				"$ready_fd" "$stderr_fd" "$signal_fd" \
				-- "${@:7}"
		fi
		shift 6
		if [ "$stderr_fd" -ge 3 ]; then
			eval "exec 2>&${stderr_fd}"
			eval "exec ${stderr_fd}>&-"
		fi
		if [ "$signal_fd" -ge 3 ]; then
			exit 126
		fi
		if [ "$ready_fd" -ge 3 ]; then
			eval "printf '\\001' >&${ready_fd}"
			eval "exec ${ready_fd}>&-"
		fi
		exec "$@"
	fi
	exec /bin/sh "$@"
}

run_delayed_child() {
	child_status_fd=$1
	marker=$2

	IFS= read -r _ || :
	eval "exec ${child_status_fd}>&-"
	sleep 0.2
	: >"$marker"
}

publish_child() {
	child_pid=$1
	mnt_namespace=$(stat -Lc %i "/proc/${child_pid}/ns/mnt") || return 1
	eval "printf '%s\n' '{ \"child-pid\": ${child_pid}, \"mnt-namespace\": ${mnt_namespace} }' >&${status_fd}"
}

if [ "$1" = "--fake-pre-exec-fail" ]; then
	printf x >>"$2"
	run_shell_child "$status_fd" -c ':' <&"$block_fd" &
	child_pid=$!
	publish_child "$child_pid" || exit 2
	eval "exec ${block_fd}>&-"
	wait "$child_pid"
	printf '%s\n' 'bwrap: synthetic setup failure' >&2
	exit 1
fi

if [ "$1" = "--fake-retry-state" ]; then
	state=$2
	shift 3
	if [ ! -e "$state" ]; then
		: >"$state"
		run_shell_child "$status_fd" -c ':' <&"$block_fd" &
		child_pid=$!
		publish_child "$child_pid" || exit 2
		eval "exec ${block_fd}>&-"
		wait "$child_pid"
		printf '%s\n' 'bwrap: transient overlay failure' >&2
		exit 1
	fi
fi

if [ "$1" = "--fake-always-pre-exec-fail" ]; then
	attempts=$2
	printf x >>"$attempts"
	run_shell_child "$status_fd" -c ':' <&"$block_fd" &
	child_pid=$!
	publish_child "$child_pid" || exit 2
	eval "exec ${block_fd}>&-"
	wait "$child_pid"
	printf '%s\n' 'bwrap: transient overlay failure' >&2
	exit 1
fi

if [ "$1" = "--fake-ready-monitor-interrupt" ]; then
	shift 2
	run_shell_child "$status_fd" "$@" <&"$block_fd" &
	child_pid=$!
	publish_child "$child_pid" || exit 2
	eval "exec ${block_fd}>&-"
	wait "$child_pid"
	exit 130
fi

if [ "$1" = "--fake-delayed-sandbox-exit" ]; then
	marker=$2
	run_delayed_child "$status_fd" "$marker" <&"$block_fd" &
	child_pid=$!
	publish_child "$child_pid" || exit 2
	eval "exec ${block_fd}>&-"
	sleep 0.05
	eval "printf '%s\n' '{ \"exit-code\": 0 }' >&${status_fd}"
	exit 0
fi

run_shell_child "$status_fd" "$@" <&"$block_fd" &
child_pid=$!
trap '' INT TERM HUP QUIT
publish_child "$child_pid" || exit 2
eval "exec ${block_fd}>&-"

wait "$child_pid"
code=$?
eval "printf '%s\n' '{ \"exit-code\": ${code} }' >&${status_fd}"
exit "$code"
