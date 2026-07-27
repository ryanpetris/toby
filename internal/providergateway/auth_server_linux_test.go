//go:build linux

package providergateway

// Exercises the protected authorization Unix socket, exact descriptor
// retention, HTTP bounds, collision handling, and no-follow teardown.

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestAuthServerServesProtectedSocketAndUnlinksOnClose(t *testing.T) {
	path := authServerTestPath(t)
	store := newRouteStore()
	item := testRoute("route-one", "cap-one", "credential-one")
	if _, err := store.add([]route{item}); err != nil {
		t.Fatal(err)
	}
	if err := store.activate([]string{item.ID}); err != nil {
		t.Fatal(err)
	}
	if err := store.setGenerationToken("generation-one"); err != nil {
		t.Fatal(err)
	}

	server, err := newAuthServer(
		t.Context(),
		path,
		store,
		authServerOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}

	parent, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if parent.Mode().Perm() != 0o700 {
		t.Fatalf(
			"auth socket parent mode = %03o, want 700",
			parent.Mode().Perm(),
		)
	}
	socketInfo, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if socketInfo.Mode()&os.ModeSocket == 0 ||
		socketInfo.Mode().Perm() != 0o600 {
		t.Fatalf(
			"auth socket mode = %v, want socket 0600",
			socketInfo.Mode(),
		)
	}

	device, inode := server.Generation()
	file, err := server.File()
	if err != nil {
		t.Fatal(err)
	}
	fileInfo, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	status := fileInfo.Sys().(*syscall.Stat_t)
	if uint64(status.Dev) != device || status.Ino != inode {
		t.Fatalf(
			"retained auth socket = %d:%d, want %d:%d",
			status.Dev,
			status.Ino,
			device,
			inode,
		)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	client := authServerHTTPClient(path)
	defer client.CloseIdleConnections()
	request := authorizedRequest(t, item, "generation-one")
	request.URL.Scheme = "http"
	request.URL.Host = "auth.toby.invalid"
	request.RequestURI = ""
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, readErr := io.Copy(io.Discard, response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf(
			"close auth response: %v",
			errors.Join(readErr, closeErr),
		)
	}
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf(
			"auth response = %d, want 204",
			response.StatusCode,
		)
	}

	closeContext, cancel := context.WithTimeout(
		context.Background(),
		time.Second,
	)
	defer cancel()
	if err := server.Close(closeContext); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("auth socket remains after close: %v", err)
	}
	select {
	case <-server.Done():
	default:
		t.Fatal("auth server did not report stopped")
	}
}

func TestAuthServerRejectsSecondOwnerAndRequestBodies(t *testing.T) {
	path := authServerTestPath(t)
	store := newRouteStore()
	item := testRoute("route-one", "cap-one", "credential-one")
	if _, err := store.add([]route{item}); err != nil {
		t.Fatal(err)
	}
	if err := store.activate([]string{item.ID}); err != nil {
		t.Fatal(err)
	}
	if err := store.setGenerationToken("generation-one"); err != nil {
		t.Fatal(err)
	}

	server, err := newAuthServer(
		t.Context(),
		path,
		store,
		authServerOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(
			context.Background(),
			time.Second,
		)
		defer cancel()
		if err := server.Close(ctx); err != nil {
			t.Error(err)
		}
	}()

	if duplicate, err := newAuthServer(
		t.Context(),
		path,
		store,
		authServerOptions{},
	); err == nil {
		_ = duplicate.Close(t.Context())
		t.Fatal("second auth server acquired the active socket")
	}

	request := authorizedRequest(t, item, "generation-one")
	request.URL.Scheme = "http"
	request.URL.Host = "auth.toby.invalid"
	request.RequestURI = ""
	request.Body = io.NopCloser(strings.NewReader("x"))
	request.ContentLength = 1
	client := authServerHTTPClient(path)
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf(
			"auth request with body status = %d, want 404",
			response.StatusCode,
		)
	}
}

func authServerTestPath(t *testing.T) string {
	t.Helper()

	root := filepath.Join(t.TempDir(), "runtime")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	toby := filepath.Join(root, "toby")
	if err := os.Mkdir(toby, 0o700); err != nil {
		t.Fatal(err)
	}

	return filepath.Join(toby, "caddy", "auth.sock")
}

func authServerHTTPClient(path string) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(
				ctx context.Context,
				_ string,
				_ string,
			) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(
					ctx,
					"unix",
					path,
				)
			},
		},
		Timeout: time.Second,
	}
}
