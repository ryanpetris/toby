package kit

// Reads launch yolo state from the effective host configuration.

import appconfig "petris.dev/toby/internal/config/app"

// YoloFromConfig reports the current launch's yolo setting.
func YoloFromConfig(config *appconfig.LaunchHolder) func() bool {
	return func() bool {
		if config == nil {
			return false
		}
		current := config.Current()
		return current != nil && current.Settings().YoloEnabled()
	}
}
