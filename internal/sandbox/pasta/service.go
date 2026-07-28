package pasta

// Starts Pasta against one held sandbox init and waits for documented
// initialization readiness.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"petris.dev/toby/internal/diagnostic"
)

const (
	executableName          = "pasta"
	namespaceExecutableName = "nsenter"
	readinessFilename       = "pasta.pid"
	readinessPollInterval   = 10 * time.Millisecond
	readinessTimeout        = 10 * time.Second
	startupCleanupTimeout   = time.Second
)

// StartOptions identifies the held network namespace and its run-scoped
// readiness path.
type StartOptions struct {
	TargetPID        int
	RuntimeDirectory string
	DNSForward       string
}

// Service starts Pasta connections without requiring Pasta until a private
// network is actually requested.
type Service struct {
	executable          string
	namespaceExecutable string
	waitForNamespace    func(context.Context, int) error
	readinessPoll       time.Duration
	readinessLimit      time.Duration
	logger              *diagnostic.Logger
}

// NewService constructs the process-wide Pasta launch service.
func NewService(diagnostics *diagnostic.Service) (*Service, error) {
	if diagnostics == nil {
		return nil, fmt.Errorf("pasta diagnostic service is required")
	}

	return &Service{
		waitForNamespace: waitForChildUserNamespace,
		readinessPoll:    readinessPollInterval,
		readinessLimit:   readinessTimeout,
		logger:           diagnostics.Logger("sandbox.pasta"),
	}, nil
}

// Start connects one held sandbox network namespace and waits until Pasta has
// configured it.
func (s *Service) Start(
	ctx context.Context,
	options StartOptions,
) (result Process, returnErr error) {
	if s == nil {
		return nil, fmt.Errorf("pasta service is not configured")
	}
	if ctx == nil {
		return nil, fmt.Errorf("start Pasta: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if options.TargetPID <= 0 {
		return nil, fmt.Errorf("pasta target PID must be positive")
	}
	if options.RuntimeDirectory == "" {
		return nil, fmt.Errorf("pasta runtime directory is required")
	}
	if options.DNSForward == "" {
		return nil, fmt.Errorf("pasta DNS forward address is required")
	}

	waitForNamespace := s.waitForNamespace
	if waitForNamespace == nil {
		waitForNamespace = waitForChildUserNamespace
	}
	if err := waitForNamespace(ctx, options.TargetPID); err != nil {
		return nil, fmt.Errorf(
			"wait for Bubblewrap user namespace: %w",
			err,
		)
	}

	executable, err := resolveExecutable(
		s.executable,
		executableName,
		"passt",
	)
	if err != nil {
		return nil, err
	}
	namespaceExecutable, err := resolveExecutable(
		s.namespaceExecutable,
		namespaceExecutableName,
		"util-linux",
	)
	if err != nil {
		return nil, err
	}

	pidPath := filepath.Join(
		options.RuntimeDirectory,
		readinessFilename,
	)
	output := &diagnosticOutput{}
	command := exec.Command(
		namespaceExecutable,
		"--target", strconv.Itoa(options.TargetPID),
		"--user",
		"--user-parent",
		"--preserve-credentials",
		"--",
		executable,
		"--foreground",
		"--quiet",
		"--config-net",
		"--no-map-gw",
		"--tcp-ports", "none",
		"--udp-ports", "none",
		"--tcp-ns", "none",
		"--udp-ns", "none",
		"--dns-forward", options.DNSForward,
		"--pid", pidPath,
		"--netns", filepath.Join(
			"/proc",
			strconv.Itoa(options.TargetPID),
			"ns",
			"net",
		),
	)
	command.Env = []string{}
	command.Stdout = output
	command.Stderr = output
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start Pasta: %w", err)
	}

	process := newProcess(command, output)
	defer func() {
		if returnErr == nil {
			return
		}

		cleanupCtx, cancel := context.WithTimeout(
			context.Background(),
			startupCleanupTimeout,
		)
		defer cancel()
		s.logger.DebugError(
			"kill Pasta after startup failure",
			process.Kill(cleanupCtx),
		)
		select {
		case <-process.Done():
		case <-cleanupCtx.Done():
			s.logger.DebugError(
				"wait for Pasta after startup failure",
				cleanupCtx.Err(),
			)
		}
	}()

	readyCtx, cancel := context.WithTimeout(ctx, s.readinessLimit)
	defer cancel()
	ticker := time.NewTicker(s.readinessPoll)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(pidPath); err == nil {
			select {
			case <-process.Done():
				return nil, pastaStartupExit(process)
			default:
				return process, nil
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf(
				"inspect Pasta readiness file: %w",
				err,
			)
		}

		select {
		case <-process.Done():
			return nil, pastaStartupExit(process)
		case <-readyCtx.Done():
			return nil, fmt.Errorf(
				"wait for Pasta readiness: %w",
				readyCtx.Err(),
			)
		case <-ticker.C:
		}
	}
}

func resolveExecutable(
	configured string,
	name string,
	packageName string,
) (string, error) {
	executable := configured
	if executable == "" {
		var err error
		executable, err = exec.LookPath(name)
		if err != nil {
			return "", fmt.Errorf(
				"find %s executable; install the %s package: %w",
				name,
				packageName,
				err,
			)
		}
	}

	executable, err := filepath.Abs(executable)
	if err != nil {
		return "", fmt.Errorf("resolve %s executable: %w", name, err)
	}
	return executable, nil
}

func pastaStartupExit(process Process) error {
	if err := process.Err(); err != nil {
		return fmt.Errorf("pasta exited before network readiness: %w", err)
	}
	return fmt.Errorf("pasta exited before network readiness")
}
