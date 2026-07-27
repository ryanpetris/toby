//go:build linux

package sandbox

// Exercises the resource connector's exact pre-composition process path.

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const resourceConnectorProcessHelperEnvironment = "TOBY_APP_RESOURCE_CONNECTOR_TEST_HELPER"

func TestMain(m *testing.M) {
	if os.Getenv(resourceConnectorProcessHelperEnvironment) == "1" {
		os.Exit(Execute(os.Args, os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

func TestResourceConnectorProcessDoesNotDependOnProcessConfiguration(
	t *testing.T,
) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	command := exec.CommandContext(
		ctx,
		executable,
		"resource",
		"connect",
		"--",
		"early-dispatch-test",
	)
	command.Env = resourceConnectorProcessEnvironment()
	command.Stdin = strings.NewReader("")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr

	err = command.Run()
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Fatalf("connector process error = %v, want exit error", err)
	}
	if code := exitError.ExitCode(); code != 1 {
		t.Fatalf("connector process exit code = %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("connector stdout = %q, want empty", stdout.String())
	}

	diagnostic := stderr.String()
	if strings.Contains(diagnostic, "XDG_CONFIG_HOME") {
		t.Fatalf(
			"connector process loaded application paths: %q",
			diagnostic,
		)
	}
	if !strings.Contains(diagnostic, "sandbox") {
		t.Fatalf(
			"connector process diagnostic = %q, want sandbox endpoint failure",
			diagnostic,
		)
	}
}

func resourceConnectorProcessEnvironment() []string {
	const (
		sandboxName = "TOBY_SANDBOX"
		configName  = "XDG_CONFIG_HOME"
		helperName  = resourceConnectorProcessHelperEnvironment
	)

	environment := make([]string, 0, len(os.Environ())+3)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		switch name {
		case sandboxName, configName, helperName:
			continue
		default:
			environment = append(environment, entry)
		}
	}

	return append(
		environment,
		configName+"=relative",
		helperName+"=1",
	)
}
