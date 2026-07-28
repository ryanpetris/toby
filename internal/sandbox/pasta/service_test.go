package pasta

// Exercises exact Pasta invocation, readiness, failure diagnostics, and
// bounded startup cancellation.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestServiceStartsExactPrivateNetworkInvocation(t *testing.T) {
	service := testService(t, "fake-pasta.sh")
	runtimeDirectory := t.TempDir()
	networkProcess, err := service.Start(t.Context(), StartOptions{
		TargetPID:        1234,
		RuntimeDirectory: runtimeDirectory,
		DNSForward:       "198.51.100.53",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = networkProcess.Kill(context.Background())
		<-networkProcess.Done()
	})

	concrete := networkProcess.(*process)
	concrete.mu.Lock()
	got := append([]string(nil), concrete.command.Args[1:]...)
	concrete.mu.Unlock()
	want := []string{
		"--target", "1234",
		"--user",
		"--user-parent",
		"--preserve-credentials",
		"--",
		service.executable,
		"--foreground",
		"--quiet",
		"--config-net",
		"--no-map-gw",
		"--tcp-ports", "none",
		"--udp-ports", "none",
		"--tcp-ns", "none",
		"--udp-ns", "none",
		"--dns-forward", "198.51.100.53",
		"--pid", filepath.Join(runtimeDirectory, readinessFilename),
		"--netns", "/proc/1234/ns/net",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Pasta arguments = %#v, want %#v", got, want)
	}

	if err := networkProcess.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-networkProcess.Done():
	case <-time.After(time.Second):
		t.Fatal("Pasta did not stop")
	}
	if err := networkProcess.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestServiceReturnsPastaStartupDiagnostics(t *testing.T) {
	service := testService(t, "fake-pasta-fail.sh")
	_, err := service.Start(t.Context(), StartOptions{
		TargetPID:        1234,
		RuntimeDirectory: t.TempDir(),
		DNSForward:       "198.51.100.53",
	})
	if err == nil ||
		!strings.Contains(err.Error(), "synthetic Pasta startup failure") {
		t.Fatalf("startup error = %v", err)
	}
}

func TestServiceKillsPastaWhenReadinessTimesOut(t *testing.T) {
	service := testService(t, "fake-pasta-no-ready.sh")
	service.readinessLimit = 50 * time.Millisecond
	runtimeDirectory := t.TempDir()
	_, err := service.Start(t.Context(), StartOptions{
		TargetPID:        1234,
		RuntimeDirectory: runtimeDirectory,
		DNSForward:       "198.51.100.53",
	})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("readiness error = %v, want deadline", err)
	}

	data, err := os.ReadFile(filepath.Join(
		runtimeDirectory,
		readinessFilename+".started",
	))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(
		filepath.Join("/proc", strconv.Itoa(pid)),
	); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Pasta process %d survived timeout: %v", pid, err)
	}
}

func testService(t *testing.T, fixture string) *Service {
	t.Helper()

	executable, err := filepath.Abs(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	namespaceExecutable, err := filepath.Abs(
		filepath.Join("testdata", "fake-nsenter.sh"),
	)
	if err != nil {
		t.Fatal(err)
	}
	return &Service{
		executable:          executable,
		namespaceExecutable: namespaceExecutable,
		waitForNamespace: func(context.Context, int) error {
			return nil
		},
		readinessPoll:  time.Millisecond,
		readinessLimit: time.Second,
	}
}
