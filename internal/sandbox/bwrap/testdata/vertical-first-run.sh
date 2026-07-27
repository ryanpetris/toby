set -eu
for fd in 3 4 5; do
	test ! -e "/proc/self/fd/$fd"
done
test ! -e "/proc/self/fd/$1"
chmod 0755 /run/toby
printf 'root-layer' > /m2-root-marker
chmod 000 /m2-root-marker