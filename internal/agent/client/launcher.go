package client

// Defines the detached tobyd launcher used for agent autostart.

import (
	"context"
	"fmt"
	"os"
	"strings"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/executable"
)

var agentEnvironmentNames = map[string]struct{}{
	"ALL_PROXY":         {},
	"HOME":              {},
	"HTTPS_PROXY":       {},
	"HTTP_PROXY":        {},
	"LANG":              {},
	"LANGUAGE":          {},
	"LOGNAME":           {},
	"NIX_SSL_CERT_FILE": {},
	"NO_PROXY":          {},
	"PATH":              {},
	"SSL_CERT_DIR":      {},
	"SSL_CERT_FILE":     {},
	"TMPDIR":            {},
	"TOBY_LOG_FORMAT":   {},
	"TOBY_LOG_LEVEL":    {},
	"TZ":                {},
	"USER":              {},
	"XDG_CACHE_HOME":    {},
	"XDG_CONFIG_HOME":   {},
	"XDG_DATA_HOME":     {},
	"XDG_PROJECTS_DIR":  {},
	"XDG_RUNTIME_DIR":   {},
	"all_proxy":         {},
	"http_proxy":        {},
	"https_proxy":       {},
	"no_proxy":          {},
}

// CommandLauncher starts the installed tobyd agent binary.
type CommandLauncher struct {
	Executable  string
	Environment []string
	Logger      *diagnostic.Logger
}

var _ Launcher = (*CommandLauncher)(nil)

// NewCommandLauncher resolves tobyd and preserves the launch
// environment needed for XDG paths and later background-resource helpers.
func NewCommandLauncher(
	logger *diagnostic.Logger,
) (*CommandLauncher, error) {
	agentExecutable, err := executable.Resolve(executable.Agent)
	if err != nil {
		return nil, fmt.Errorf(
			"resolve tobyd for agent autostart: %w",
			err,
		)
	}

	return &CommandLauncher{
		Executable:  agentExecutable,
		Environment: agentEnvironment(os.Environ()),
		Logger:      logger,
	}, nil
}

// Launch starts a detached agent and returns after exec succeeds.
func (l *CommandLauncher) Launch(ctx context.Context) error {
	if l == nil {
		return fmt.Errorf("agent command launcher is nil")
	}
	if ctx == nil {
		return fmt.Errorf("agent command launcher context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if l.Executable == "" {
		return fmt.Errorf("agent command launcher executable is required")
	}

	return launchDetached(l.Executable, l.Environment, l.Logger)
}

func agentEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment))
	seen := make(map[string]struct{}, len(environment))
	for index := len(environment) - 1; index >= 0; index-- {
		entry := environment[index]
		name, _, found := strings.Cut(entry, "=")
		if !found || name == "" {
			continue
		}
		if _, allowed := agentEnvironmentNames[name]; !allowed &&
			!strings.HasPrefix(name, "LC_") {
			continue
		}
		if _, duplicate := seen[name]; duplicate {
			continue
		}

		seen[name] = struct{}{}
		result = append(result, entry)
	}
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}

	return result
}
