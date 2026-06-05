package mount

// Setup-phase execution: Executor runs a command in the sandbox during Setup,
// SetupFunc is a per-volume initializer, and defaultChown is the default
// initializer (chown the volume's setup path to the host user).

import (
	"context"
	"fmt"
	"os"
)

// Executor executes a command in the sandbox during the Setup phase.
type Executor interface {
	Exec(ctx context.Context, argv []string, root bool) (int, error)
}

// SetupFunc initializes a freshly-created volume as root during the Setup phase.
// setupPath is where the volume is mounted in the setup container. A nil SetupFunc
// uses the default behavior: chown the path to the host user.
type SetupFunc func(ctx context.Context, setupPath string, run Executor) error

func defaultChown(ctx context.Context, paths []string, run Executor) error {
	argv := append([]string{"chown", "-R", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())}, paths...)
	_, err := run.Exec(ctx, argv, true)
	return err
}
