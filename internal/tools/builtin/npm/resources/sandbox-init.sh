#!/bin/sh
set -eu

if ! command -v npm >/dev/null 2>&1; then
	printf 'npm is not available inside the sandbox\n' >&2
	exit 127
fi

if [ -d "$NPM_CONFIG_PREFIX/bin" ] && [ -d "$NPM_CONFIG_PREFIX/lib/node_modules" ]; then
	exit 0
fi

mkdir -p "$NPM_CONFIG_PREFIX/bin" "$NPM_CONFIG_PREFIX/lib/node_modules" "$NPM_CONFIG_CACHE"
