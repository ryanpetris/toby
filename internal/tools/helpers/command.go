package helpers

import (
	"context"
	"errors"
)

func CommandExists[Options any](ctx context.Context, exec func(context.Context, []string, Options) (int, error), opts Options, command string) (bool, error) {
	rc, err := exec(ctx, []string{"which", command}, opts)
	if err != nil {
		var coded interface{ ExitCode() int }
		if errors.As(err, &coded) && err.Error() == "" {
			return false, nil
		}
		return false, err
	}
	return rc == 0, nil
}
