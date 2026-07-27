//go:build linux

package socketrelay

// Exercises the retained supplementary-group authority against a real
// root-owned Docker socket when explicitly requested.

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/sandbox/layout"
)

func TestRelayRetainsDockerSupplementaryGroupAuthority(t *testing.T) {
	if os.Getenv("TOBY_DOCKER_GROUP_INTEGRATION") != "1" {
		t.Skip(
			"set TOBY_DOCKER_GROUP_INTEGRATION=1 with a group-authorized Docker socket",
		)
	}

	const dockerSocket = "/var/run/docker.sock"
	var status unix.Stat_t
	if err := unix.Stat(dockerSocket, &status); err != nil {
		t.Fatal(err)
	}
	groups, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	if status.Uid == uint32(os.Geteuid()) ||
		status.Gid == uint32(os.Getegid()) ||
		!slices.Contains(groups, int(status.Gid)) ||
		status.Mode&0o060 != 0o060 ||
		status.Mode&0o006 != 0 {
		t.Fatalf(
			"Docker socket %d:%d mode %04o does not require a supplementary group for this process",
			status.Uid,
			status.Gid,
			status.Mode&0o777,
		)
	}

	root := openRelayTestRoot(t)
	defer root.Close()
	registry, err := NewRegistry([]Request{{
		HostSocket:    dockerSocket,
		SandboxSocket: layout.Runtime + "/docker.sock",
	}})
	if err != nil {
		t.Fatal(err)
	}
	set, err := registry.Start(t.Context(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer set.Close()

	connection, err := net.DialUnix(
		"unix",
		nil,
		&net.UnixAddr{
			Name: set.RuntimeAssets()[0].HostPath,
			Net:  "unix",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.SetDeadline(
		time.Now().Add(5 * time.Second),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprint(
		connection,
		"GET /_ping HTTP/1.0\r\nHost: docker\r\n\r\n",
	); err != nil {
		t.Fatal(err)
	}
	statusLine, err := bufio.NewReader(connection).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(statusLine, " 200 ") {
		t.Fatalf("Docker ping response = %q", statusLine)
	}
}
