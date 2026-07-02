// fx wiring: provide the Watcher and run its poll loop for the daemon's lifetime.

package configwatch

import (
	"context"

	"go.uber.org/fx"
)

// Module provides the config Watcher and starts its poll loop.
func Module() fx.Option {
	return fx.Module("configwatch",
		fx.Provide(New),
		fx.Invoke(runWatcher),
	)
}

func runWatcher(lc fx.Lifecycle, w *Watcher) {
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			go w.watch()
			return nil
		},
		OnStop: func(context.Context) error {
			close(w.stop)
			return nil
		},
	})
}
