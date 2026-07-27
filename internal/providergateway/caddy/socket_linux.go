//go:build linux

package caddy

// Verifies Caddy sockets descriptor-relatively beneath the exact retained
// generation directory.

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/diagnostic"
)

func verifySocket(
	runtime *os.File,
	name string,
) error {
	if runtime == nil || !validSocketName(name) {
		return fmt.Errorf("caddy socket capability is invalid")
	}

	var stat unix.Stat_t
	if err := unix.Fstatat(
		int(runtime.Fd()),
		name,
		&stat,
		unix.AT_SYMLINK_NOFOLLOW,
	); err != nil {
		return fmt.Errorf("caddy socket is unavailable")
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFSOCK ||
		stat.Nlink != 1 {
		return fmt.Errorf("caddy socket identity is invalid")
	}

	return nil
}

func dialSocket(
	ctx context.Context,
	runtime *os.File,
	name string,
) (*net.UnixConn, error) {
	if ctx == nil {
		return nil, fmt.Errorf("caddy socket context is nil")
	}
	if err := verifySocket(runtime, name); err != nil {
		return nil, err
	}

	address := filepath.Join(
		"/proc/self/fd",
		strconv.FormatUint(uint64(runtime.Fd()), 10),
		name,
	)
	var dialer net.Dialer
	connection, err := dialer.DialContext(ctx, "unix", address)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("caddy socket is unavailable")
	}
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		err := fmt.Errorf(
			"caddy socket returned a non-Unix connection",
		)
		diagnostic.DiscardError(
			"the Caddy socket operation already failed",
			"close rejected Caddy socket connection",
			connection.Close(),
		)
		return nil, err
	}

	return unixConnection, nil
}

func validSocketName(name string) bool {
	return name != "" &&
		name != "." &&
		name != ".." &&
		filepath.Base(name) == name
}
