// Package main provides the static Docker CLI fixture used by the Bubblewrap
// socket-relay integration test.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	sandboxDockerHost = "unix:///run/toby/docker.sock"
	apiVersion        = "1.47"
)

func main() {
	switch filepath.Base(os.Args[0]) {
	case "which":
		runWhich()
	case "docker":
		runDocker()
	default:
		fail("unsupported fixture command %q", filepath.Base(os.Args[0]))
	}
}

func runWhich() {
	if len(os.Args) != 2 || os.Args[1] != "docker" {
		os.Exit(1)
	}

	fmt.Println("/usr/bin/docker")
}

func runDocker() {
	if len(os.Args) != 4 ||
		os.Args[1] != "version" ||
		os.Args[2] != "--format" ||
		os.Args[3] != "{{.Server.Version}}" {
		fail("unexpected Docker arguments %q", os.Args[1:])
	}
	if host := os.Getenv("DOCKER_HOST"); host != sandboxDockerHost {
		fail("DOCKER_HOST = %q, want %q", host, sandboxDockerHost)
	}
	if contextName := os.Getenv("DOCKER_CONTEXT"); contextName != "" {
		fail("DOCKER_CONTEXT = %q, want empty", contextName)
	}
	config, err := os.ReadFile("/toby/home/.docker/config.json")
	if err != nil {
		fail("read host Docker configuration: %v", err)
	}
	if string(config) != "host-docker-config\n" {
		fail("host Docker configuration = %q", config)
	}

	socket := strings.TrimPrefix(sandboxDockerHost, "unix://")
	transport := &http.Transport{
		DialContext: func(
			ctx context.Context,
			network string,
			address string,
		) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}
	defer transport.CloseIdleConnections()

	client := &http.Client{
		Transport: transport,
		Timeout:   5 * time.Second,
	}
	ping(client)

	response, err := client.Get(
		"http://docker/v" + apiVersion + "/version",
	)
	if err != nil {
		fail("request Docker version: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		fail("Docker version status = %s", response.Status)
	}

	var document struct {
		Version string `json:"Version"`
	}
	if err := json.NewDecoder(response.Body).Decode(&document); err != nil {
		fail("decode Docker version: %v", err)
	}
	if document.Version == "" {
		fail("Docker version response is empty")
	}

	fmt.Println(document.Version)
}

func ping(client *http.Client) {
	response, err := client.Get("http://docker/_ping")
	if err != nil {
		fail("ping Docker endpoint: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		fail("Docker ping status = %s", response.Status)
	}
	if response.Header.Get("API-Version") != apiVersion {
		fail(
			"Docker API version = %q, want %q",
			response.Header.Get("API-Version"),
			apiVersion,
		)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
