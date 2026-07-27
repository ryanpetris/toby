set -eu
printf '%s %s\n' "$$" "$(readlink /proc/self/ns/pid)" > "$HOME/$1"
shift
exec "$@"