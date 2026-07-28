//go:build linux

package pasta

// Proves real Bubblewrap launch-gate integration, synthetic DNS forwarding,
// and outbound HTTPS on an explicitly enabled Linux host.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"petris.dev/toby/internal/sandbox/bwrap"
	"petris.dev/toby/internal/sandbox/hostconfig"
)

func TestPastaConnectsHeldBubblewrapNetwork(t *testing.T) {
	if os.Getenv("TOBY_PASTA_INTEGRATION") != "1" {
		t.Skip("set TOBY_PASTA_INTEGRATION=1 on the target Linux host")
	}

	runtimeDirectory := t.TempDir()
	resolverPath := filepath.Join(runtimeDirectory, "resolv.conf")
	if err := os.WriteFile(
		resolverPath,
		[]byte(
			"nameserver "+
				hostconfig.PrivateResolverAddress+
				"\n",
		),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	output, err := os.Create(filepath.Join(runtimeDirectory, "output"))
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()

	executor, err := bwrap.NewExecutor(bwrap.ExecutorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer executor.Close()
	service := &Service{
		waitForNamespace: waitForChildUserNamespace,
		readinessPoll:    readinessPollInterval,
		readinessLimit:   readinessTimeout,
	}

	args := []string{
		"--unshare-user",
		"--uid", strconv.Itoa(os.Geteuid()),
		"--gid", strconv.Itoa(os.Getegid()),
		"--unshare-pid",
		"--unshare-ipc",
		"--unshare-uts",
		"--die-with-parent",
		"--unshare-net",
		"--ro-bind", "/", "/",
		"--bind", resolverPath, "/etc/resolv.conf",
		"--proc", "/proc",
		"--dev", "/dev",
		"--clearenv",
		"--setenv", "PATH", "/usr/bin:/bin",
		"--",
		"/bin/sh", "-c",
		"getent ahostsv4 example.com && curl --fail --silent --show-error https://example.com",
	}
	var network Process
	process, err := executor.StartBackground(
		t.Context(),
		&bwrap.Invocation{
			Args: args,
			Mode: bwrap.ExecutionNonInteractive,
		},
		bwrap.ProcessIO{
			Stdout: output,
			Stderr: output,
		},
		func(ctx context.Context, pid int) error {
			var startErr error
			network, startErr = service.Start(ctx, StartOptions{
				TargetPID:        pid,
				RuntimeDirectory: runtimeDirectory,
				DNSForward:       hostconfig.PrivateResolverAddress,
			})
			return startErr
		},
	)
	if err != nil {
		t.Fatalf(
			"start Pasta-connected Bubblewrap: %v; output=%s",
			err,
			readOutput(t, output),
		)
	}
	t.Cleanup(func() {
		_ = process.Kill(context.Background())
		<-process.Done()
		if network != nil {
			_ = network.Kill(context.Background())
			<-network.Done()
		}
	})

	select {
	case <-process.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("Bubblewrap payload did not finish")
	}
	if err := process.Err(); err != nil {
		t.Fatalf("Bubblewrap payload failed: %v; output=%s", err, readOutput(t, output))
	}

	if err := network.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-network.Done():
	case <-time.After(time.Second):
		t.Fatal("Pasta did not stop")
	}
	if err := network.Err(); err != nil {
		t.Fatal(err)
	}

	got := readOutput(t, output)
	if !strings.Contains(got, "example.com") ||
		!strings.Contains(got, "<title>Example Domain</title>") {
		t.Fatalf("unexpected network output: %s", got)
	}
}

func readOutput(t *testing.T, output *os.File) string {
	t.Helper()

	if _, err := output.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output.Name())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return string(data)
}
