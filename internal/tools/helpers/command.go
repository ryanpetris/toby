// Package helpers provides small utilities shared by the concrete tool
// implementations: command-existence probes and environment construction from
// string lists.
package helpers

import (
	"context"

	"petris.dev/toby/internal/diagnostic/exitcode"
)

// CommandExists reports whether command resolves inside the target environment.
func CommandExists[Options any](ctx context.Context, exec func(context.Context, []string, Options) (int, error), opts Options, command string) (bool, error) {
	rc, err := exec(ctx, []string{"which", command}, opts)
	if err != nil {
		if exitcode.IsSilent(err) {
			return false, nil
		}
		return false, err
	}
	return rc == 0, nil
}
