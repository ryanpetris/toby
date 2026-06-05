package run

// Launch-option defaults: ApplySandboxDefaults fills a launch's Options from the
// host config wherever the invocation left them unset.

import (
	appconfig "petris.dev/toby/config/app"
	"petris.dev/toby/tools"
)

func ApplySandboxDefaults(opts *tools.Options, config *appconfig.Service) tools.Options {
	if opts == nil {
		opts = &tools.Options{}
	}
	result := *opts
	container := config.Container()
	settings := config.Settings()
	if result.MountProfile == "" {
		result.MountProfile = settings.MountProfile
	}
	if result.Debug == nil && settings.Debug != nil {
		debug := *settings.Debug
		result.Debug = &debug
	}
	if result.Yolo == nil && settings.Yolo != nil {
		yolo := *settings.Yolo
		result.Yolo = &yolo
	}
	result.ToolMountProfiles = config.ToolMountProfiles()
	mergeStringMap(result.ToolMountProfiles, opts.ToolMountProfiles)
	result.SuppressWarnings = settings.SuppressWarnings.Clone()
	result.SuppressWarnings.Merge(opts.SuppressWarnings)
	if result.Image == "" {
		result.Image = container.Image
	}
	if !result.Build.IsSet() {
		result.Build = container.Build
	}
	return result
}

func mergeStringMap(dst, src map[string]string) {
	if len(src) == 0 {
		return
	}
	for key, value := range src {
		dst[key] = value
	}
}
