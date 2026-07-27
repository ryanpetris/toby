set -eu
test "$(cat /m2-root-marker)" = root-layer
chmod 0644 /m2-root-marker