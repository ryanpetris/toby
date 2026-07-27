set -eu
while [ "$1" != "--" ]; do
	test -f "$1" || { printf 'missing own path: %s\n' "$1" >&2; exit 90; }
	shift
done
shift
for candidate in "$@"; do
	test ! -e "$candidate" || { printf 'visible peer path: %s\n' "$candidate" >&2; exit 91; }
done