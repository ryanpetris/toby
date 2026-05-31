//go:build darwin

package bubblewrap

import "go.uber.org/fx"

func Module() fx.Option {
	return fx.Module("sandbox.bubblewrap")
}
