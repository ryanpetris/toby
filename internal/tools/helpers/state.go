package helpers

import (
	sandboxmount "petris.dev/toby/internal/sandbox/mount"
)

func ParseMountBacking(value string) (sandboxmount.Backing, error) {
	return sandboxmount.ParseBacking(value)
}

func ResolveMountHostRoot(value, home, base string) (string, error) {
	return sandboxmount.ResolveHostRoot(value, home, base)
}
