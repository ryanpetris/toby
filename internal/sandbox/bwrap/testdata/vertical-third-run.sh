set -eu
test "$(cat /m2-root-marker)" = root-layer
for fd in 3 4 5; do
	test ! -e "/proc/self/fd/$fd"
done
test ! -e "/proc/self/fd/$1"
test ! -e "/proc/self/fd/$2"
test "$(stat -c '%a' /run/toby)" = 700
printf 'home-native' > "$HOME/native.txt"
printf 'managed-native' > /toby/home/.state/value
printf 'project-native' > /toby/workspace/app/value
if printf 'forbidden' > /toby/workspace/read-only/value; then
	exit 91
fi
printf 'vertical-ok'