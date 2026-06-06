// Package launchconfig loads and resolves the per-launch config (.toby.yaml or
// --config) into a ConfiguredLaunch: the tool selection, project mounts, and
// container settings for one sandbox launch. It shares the container block and
// strict decoding with the host config (config/app) via config/container and
// config/file.
package launchconfig

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"petris.dev/toby/config"
	configfile "petris.dev/toby/config/file"
	"petris.dev/toby/diagnostic/exitcode"
	"petris.dev/toby/diagnostic/warning"
	appconfig "petris.dev/toby/internal/config/app"
	containerconfig "petris.dev/toby/internal/config/container"
	"petris.dev/toby/tools"
)

const projectLaunchConfigName = ".toby.yaml"

type Params struct {
	Registry *tools.Registry
	Paths    config.Paths
	Config   *appconfig.Service
	Stderr   io.Writer
}

type DirectLaunch struct {
	Options        tools.Options
	Overrides      appconfig.LaunchOverrides
	Extra          []string
	RequestedTools []string
}

type ConfiguredLaunch struct {
	Options        tools.Options
	Overrides      appconfig.LaunchOverrides
	Extra          []string
	RequestedTools []string
	Primary        string
}

// launchConfig is the resolved launch configuration.
type launchConfig struct {
	Name      string
	Container launchContainerConfig
	Settings  launchSettingsConfig
	Projects  []launchProjectConfig
	Workdir   string
	Tools     []launchToolConfig
}

type launchContainerConfig struct {
	Image string
	Build tools.Build
}

type launchSettingsConfig struct {
	MountProfile     string
	AutoUpgrade      bool
	SuppressWarnings warning.Suppression
	Debug            *bool
	Yolo             *bool
}

type launchToolConfig struct {
	Name         string
	Label        string
	MountProfile string
	Params       []string
	Primary      bool
}

type launchProjectConfig struct {
	Mount   tools.ProjectMount
	Label   string
	Primary bool
}

// launchSchema is the strict decode target for a launch config file.
type launchSchema struct {
	Name      string                          `json:"name" yaml:"name"`
	Container containerconfig.Config          `json:"container" yaml:"container"`
	Settings  launchSettingsSchema            `json:"settings" yaml:"settings"`
	Project   map[string]*launchProjectSchema `json:"project" yaml:"project"`
	Workdir   string                          `json:"workdir" yaml:"workdir"`
	Tool      map[string]*launchToolSchema    `json:"tool" yaml:"tool"`
}

type launchSettingsSchema struct {
	MountProfile     string   `json:"mountProfile" yaml:"mountProfile"`
	AutoUpgrade      bool     `json:"autoUpgrade" yaml:"autoUpgrade"`
	SuppressWarnings []string `json:"suppressWarnings" yaml:"suppressWarnings"`
	Debug            *bool    `json:"debug" yaml:"debug"`
	Yolo             *bool    `json:"yolo" yaml:"yolo"`
}

type launchProjectSchema struct {
	Path    *string `json:"path" yaml:"path"`
	Primary bool    `json:"primary" yaml:"primary"`
}

type launchToolSchema struct {
	MountProfile string   `json:"mountProfile" yaml:"mountProfile"`
	Params       []string `json:"params" yaml:"params"`
	Primary      bool     `json:"primary" yaml:"primary"`
}

func BuildConfiguredLaunch(params Params, configPath string, extra []string) (ConfiguredLaunch, error) {
	cfg, err := loadLaunchConfigWithPaths(configPath, params.Paths)
	if err != nil {
		return ConfiguredLaunch{}, err
	}
	if len(cfg.Projects) == 0 {
		return ConfiguredLaunch{}, exitcode.New(2, "launch config projects must not be empty")
	}
	if len(cfg.Tools) == 0 {
		return ConfiguredLaunch{}, exitcode.New(2, "launch config tools must not be empty")
	}
	configuredTools, err := resolveConfiguredTools(params.Registry, cfg.Tools)
	if err != nil {
		return ConfiguredLaunch{}, err
	}
	primaryTool, primaryToolConfig, err := resolvePrimaryConfiguredTool(params.Registry, cfg.Tools, "")
	if err != nil {
		return ConfiguredLaunch{}, err
	}
	primaryProject, err := primaryConfiguredProject(cfg.Projects)
	if err != nil {
		return ConfiguredLaunch{}, err
	}
	options := commandOptionsFromLaunchConfig(cfg)
	options.Projects = orderedProjectMounts(cfg.Projects, primaryProject)
	overrides := overridesFromLaunchConfig(cfg)
	profiles, err := resolveConfiguredToolMountProfiles(params.Registry, cfg.Tools)
	if err != nil {
		return ConfiguredLaunch{}, err
	}
	overrides.ToolMountProfiles = profiles
	return ConfiguredLaunch{
		Options:        options,
		Overrides:      overrides,
		Extra:          configuredLaunchExtra(primaryToolConfig.Params, extra),
		RequestedTools: configuredTools,
		Primary:        primaryTool,
	}, nil
}

func BuildOverlayConfiguredLaunch(params Params, configPath string, parsed DirectLaunch, primary string, primaryProject tools.ProjectMount) (ConfiguredLaunch, error) {
	cfg, err := loadLaunchConfigWithPaths(configPath, params.Paths)
	if err != nil {
		return ConfiguredLaunch{}, err
	}
	configuredTools, err := resolveConfiguredTools(params.Registry, cfg.Tools)
	if err != nil {
		return ConfiguredLaunch{}, err
	}
	primaryParams, err := configuredParamsForPrimary(params.Registry, cfg.Tools, primary)
	if err != nil {
		return ConfiguredLaunch{}, err
	}
	options := commandOptionsFromLaunchConfig(cfg)
	overrides := overridesFromLaunchConfig(cfg)
	profiles, err := resolveConfiguredToolMountProfiles(params.Registry, cfg.Tools)
	if err != nil {
		return ConfiguredLaunch{}, err
	}
	overrides.ToolMountProfiles = profiles
	if options.Env == "" {
		options.Env = parsed.Options.Env
	}
	options.Install = parsed.Options.Install
	options.Upgrade = options.Upgrade || parsed.Options.Upgrade
	options.Projects = append([]tools.ProjectMount{primaryProject}, options.Projects...)
	mergeLaunchOverrides(&overrides, parsed.Overrides)
	requestedTools := appendIfMissing(nil, primary)
	for _, name := range parsed.RequestedTools {
		requestedTools = appendIfMissing(requestedTools, name)
	}
	for _, name := range configuredTools {
		requestedTools = appendIfMissing(requestedTools, name)
	}
	return ConfiguredLaunch{
		Options:        options,
		Overrides:      overrides,
		Extra:          configuredLaunchExtra(primaryParams, parsed.Extra),
		RequestedTools: requestedTools,
		Primary:        primary,
	}, nil
}

func MaybeAutoloadProjectConfig(params Params, parsed DirectLaunch, primary string) (ConfiguredLaunch, bool, error) {
	if strings.TrimSpace(parsed.Options.Env) == "" {
		return ConfiguredLaunch{}, false, nil
	}
	project, err := ResolveDirectLaunchProject(params.Paths, parsed.Options)
	if err != nil {
		return ConfiguredLaunch{}, false, err
	}
	configPath := filepath.Join(project.Source, projectLaunchConfigName)
	info, err := os.Stat(configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ConfiguredLaunch{}, false, nil
		}
		return ConfiguredLaunch{}, false, err
	}
	if info.IsDir() {
		return ConfiguredLaunch{}, false, fmt.Errorf("%s is a directory", configPath)
	}
	settings := params.Config.Settings()
	if !settings.AutoloadProjectConfigEnabled() {
		warning.Fprintf(params.Stderr, settings.SuppressWarnings, warning.ProjectAutoloadDisabled, "found %s but settings.autoloadProjectConfig is not enabled; pass --config %s or enable settings.autoloadProjectConfig to load it automatically.", configPath, configPath)
		return ConfiguredLaunch{}, false, nil
	}
	launch, err := BuildOverlayConfiguredLaunch(params, configPath, parsed, primary, project)
	if err != nil {
		return ConfiguredLaunch{}, false, err
	}
	return launch, true, nil
}

func commandOptionsFromLaunchConfig(cfg launchConfig) tools.Options {
	return tools.Options{
		Env:      cfg.Name,
		Upgrade:  cfg.Settings.AutoUpgrade,
		Projects: projectMounts(cfg.Projects),
		Workdir:  cfg.Workdir,
	}
}

func overridesFromLaunchConfig(cfg launchConfig) appconfig.LaunchOverrides {
	return appconfig.LaunchOverrides{
		Image:            cfg.Container.Image,
		Build:            cfg.Container.Build,
		MountProfile:     cfg.Settings.MountProfile,
		SuppressWarnings: cfg.Settings.SuppressWarnings,
		Debug:            cloneBool(cfg.Settings.Debug),
		Yolo:             cloneBool(cfg.Settings.Yolo),
	}
}

func mergeLaunchOverrides(dst *appconfig.LaunchOverrides, src appconfig.LaunchOverrides) {
	if src.Image != "" {
		dst.Image = src.Image
	}
	if src.Build.IsSet() {
		dst.Build = src.Build
	}
	if src.MountProfile != "" {
		dst.MountProfile = src.MountProfile
	}
	if len(src.ToolMountProfiles) > 0 {
		if dst.ToolMountProfiles == nil {
			dst.ToolMountProfiles = map[string]string{}
		}
		for name, profile := range src.ToolMountProfiles {
			dst.ToolMountProfiles[name] = profile
		}
	}
	if src.Debug != nil {
		debug := *src.Debug
		dst.Debug = &debug
	}
	if src.Yolo != nil {
		yolo := *src.Yolo
		dst.Yolo = &yolo
	}
}

func configuredLaunchExtra(params, extra []string) []string {
	result := make([]string, 0, len(params)+len(extra))
	result = append(result, params...)
	result = append(result, extra...)
	return result
}

func loadLaunchConfigWithPaths(path string, paths config.Paths) (launchConfig, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return launchConfig{}, exitcode.New(2, "--config requires a value")
	}
	paths = launchConfigPaths(paths)
	home := paths.Home
	expanded := config.ExpandHome(path, home)
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return launchConfig{}, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return launchConfig{}, err
	}
	var schema launchSchema
	if err := configfile.Decode(data, configfile.FormatYAML, "launch config", &schema); err != nil {
		return launchConfig{}, fmt.Errorf("%s: %w", abs, err)
	}
	cfg, err := parseLaunchConfigWithPaths(schema, filepath.Dir(abs), paths)
	if err != nil {
		return launchConfig{}, fmt.Errorf("%s: %w", abs, err)
	}
	return cfg, nil
}

func parseLaunchConfigWithPaths(schema launchSchema, dir string, paths config.Paths) (launchConfig, error) {
	paths = launchConfigPaths(paths)
	var cfg launchConfig
	cfg.Name = strings.TrimSpace(schema.Name)
	settings, err := schema.Settings.resolve()
	if err != nil {
		return launchConfig{}, err
	}
	cfg.Settings = settings
	build, err := containerconfig.ResolveBuild(schema.Container.Build, dir, paths.Home)
	if err != nil {
		return launchConfig{}, err
	}
	cfg.Container = launchContainerConfig{Image: strings.TrimSpace(schema.Container.Image), Build: build}
	projects, err := resolveLaunchProjects(schema.Project, dir, paths)
	if err != nil {
		return launchConfig{}, err
	}
	cfg.Projects = projects
	cfg.Workdir = schema.Workdir
	toolConfigs, err := resolveLaunchTools(schema.Tool)
	if err != nil {
		return launchConfig{}, err
	}
	cfg.Tools = toolConfigs
	return cfg, nil
}

func (s launchSettingsSchema) resolve() (launchSettingsConfig, error) {
	cfg := launchSettingsConfig{MountProfile: strings.TrimSpace(s.MountProfile), AutoUpgrade: s.AutoUpgrade, Debug: cloneBool(s.Debug), Yolo: cloneBool(s.Yolo)}
	if s.SuppressWarnings != nil {
		suppression, err := warning.SuppressionFromList(s.SuppressWarnings, "settings.suppressWarnings")
		if err != nil {
			return launchSettingsConfig{}, err
		}
		cfg.SuppressWarnings = suppression
	}
	return cfg, nil
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func resolveLaunchProjects(raw map[string]*launchProjectSchema, dir string, paths config.Paths) ([]launchProjectConfig, error) {
	paths = launchConfigPaths(paths)
	projects := make([]launchProjectConfig, 0, len(raw))
	for _, name := range sortedKeys(raw) {
		project, err := resolveLaunchProject(raw[name], "project."+name, name, dir, paths)
		if err != nil {
			return nil, err
		}
		projects = append(projects, project)
	}
	return projects, nil
}

func resolveLaunchProject(p *launchProjectSchema, label, name string, dir string, paths config.Paths) (launchProjectConfig, error) {
	paths = launchConfigPaths(paths)
	path := ""
	pathSet := false
	primary := false
	if p != nil {
		if p.Path != nil {
			path = *p.Path
			pathSet = true
		}
		primary = p.Primary
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return launchProjectConfig{}, fmt.Errorf("%s key must not be empty", label)
	}
	source := resolveDefaultLaunchProjectPath(name, paths.ProjectRoot)
	if pathSet {
		var err error
		source, err = resolveLaunchProjectPath(path, dir, paths.Home)
		if err != nil {
			return launchProjectConfig{}, fmt.Errorf("%s.path: %w", label, err)
		}
	}
	return launchProjectConfig{Mount: tools.ProjectMount{Name: name, Source: source}, Label: label, Primary: primary}, nil
}

func resolveLaunchTools(raw map[string]*launchToolSchema) ([]launchToolConfig, error) {
	result := make([]launchToolConfig, 0, len(raw))
	for _, name := range sortedKeys(raw) {
		parsed, err := resolveLaunchTool(raw[name], "tool."+name, name)
		if err != nil {
			return nil, err
		}
		result = append(result, parsed)
	}
	return result, nil
}

func resolveLaunchTool(t *launchToolSchema, label, name string) (launchToolConfig, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return launchToolConfig{}, fmt.Errorf("%s key must not be empty", label)
	}
	if t == nil {
		return launchToolConfig{Name: name, Label: label}, nil
	}
	params := append([]string(nil), t.Params...)
	return launchToolConfig{Name: name, Label: label, MountProfile: strings.TrimSpace(t.MountProfile), Params: params, Primary: t.Primary}, nil
}

func resolveLaunchProjectPath(path, dir, home string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("must not be empty")
	}
	path = config.ExpandHome(path, home)
	if filepath.IsAbs(path) {
		return path, nil
	}
	if path == "." {
		return dir, nil
	}
	return joinConfigRelativePath(dir, path), nil
}

func resolveDefaultLaunchProjectPath(name, projectRoot string) string {
	return joinConfigRelativePath(projectRoot, filepath.FromSlash(name))
}

func ResolveDirectLaunchProject(paths config.Paths, opts tools.Options) (tools.ProjectMount, error) {
	if strings.TrimSpace(opts.Env) == "" {
		return tools.ProjectMount{}, exitcode.New(2, "environment name is required")
	}
	raw := opts.Project
	if strings.TrimSpace(raw) == "" {
		raw = filepath.Join(paths.ProjectRoot, opts.Env)
	} else {
		raw = config.ExpandHome(raw, paths.Home)
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return tools.ProjectMount{}, err
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return tools.ProjectMount{}, exitcode.New(1, "failed to resolve project directory: %s does not exist", raw)
	}
	if _, err := relativeToProjectRoot(paths.ProjectRoot, abs); err != nil {
		return tools.ProjectMount{}, exitcode.New(1, "project directory must be under %s: %s", paths.ProjectRoot, err)
	}
	name := strings.TrimSpace(filepath.Base(abs))
	if name == "" || name == "." || name == ".." {
		return tools.ProjectMount{}, exitcode.New(2, "invalid project name: %q", name)
	}
	return tools.ProjectMount{Name: name, Source: abs}, nil
}

func relativeToProjectRoot(base, path string) (string, error) {
	absBase, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(absBase, absPath)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("%s must be equal to or inside %s", absPath, absBase)
	}
	return rel, nil
}

func joinConfigRelativePath(dir, path string) string {
	separator := string(filepath.Separator)
	if strings.HasSuffix(dir, separator) {
		return dir + path
	}
	return dir + separator + path
}

func sortedKeys[T any](items map[string]T) []string {
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func projectMounts(projects []launchProjectConfig) []tools.ProjectMount {
	mounts := make([]tools.ProjectMount, 0, len(projects))
	for _, project := range projects {
		mounts = append(mounts, project.Mount)
	}
	return mounts
}

func orderedProjectMounts(projects []launchProjectConfig, primary launchProjectConfig) []tools.ProjectMount {
	mounts := make([]tools.ProjectMount, 0, len(projects))
	mounts = append(mounts, primary.Mount)
	for _, project := range projects {
		if project.Label == primary.Label {
			continue
		}
		mounts = append(mounts, project.Mount)
	}
	return mounts
}

func primaryConfiguredProject(projects []launchProjectConfig) (launchProjectConfig, error) {
	if len(projects) == 0 {
		return launchProjectConfig{}, exitcode.New(2, "launch config projects must not be empty")
	}
	if len(projects) == 1 {
		return projects[0], nil
	}
	var primary *launchProjectConfig
	for i := range projects {
		if !projects[i].Primary {
			continue
		}
		if primary != nil {
			return launchProjectConfig{}, exitcode.New(2, "launch config projects must have only one primary project")
		}
		primary = &projects[i]
	}
	if primary == nil {
		return launchProjectConfig{}, exitcode.New(2, "launch config projects must set primary: true when multiple projects are configured")
	}
	return *primary, nil
}

func resolveConfiguredTools(registry *tools.Registry, configured []launchToolConfig) ([]string, error) {
	if registry == nil {
		return nil, fmt.Errorf("tool registry is not configured")
	}
	resolved := make([]string, 0, len(configured))
	for _, item := range configured {
		toolName, err := resolveConfiguredTool(registry, item.Name)
		if err != nil {
			return nil, err
		}
		resolved = append(resolved, toolName)
	}
	return resolved, nil
}

func resolvePrimaryConfiguredTool(registry *tools.Registry, configured []launchToolConfig, cliPrimary string) (string, launchToolConfig, error) {
	if registry == nil {
		return "", launchToolConfig{}, fmt.Errorf("tool registry is not configured")
	}
	if strings.TrimSpace(cliPrimary) != "" {
		params, err := configuredParamsForPrimary(registry, configured, cliPrimary)
		if err != nil {
			return "", launchToolConfig{}, err
		}
		return cliPrimary, launchToolConfig{Name: cliPrimary, Params: params}, nil
	}
	if len(configured) == 0 {
		return "", launchToolConfig{}, exitcode.New(2, "launch config tools must not be empty")
	}
	var primary *launchToolConfig
	if len(configured) == 1 {
		primary = &configured[0]
	} else {
		for i := range configured {
			if !configured[i].Primary {
				continue
			}
			if primary != nil {
				return "", launchToolConfig{}, exitcode.New(2, "launch config tools must have only one primary tool")
			}
			primary = &configured[i]
		}
		if primary == nil {
			return "", launchToolConfig{}, exitcode.New(2, "launch config tools must set primary: true when multiple tools are configured")
		}
	}
	primaryName, err := resolveConfiguredTool(registry, primary.Name)
	if err != nil {
		return "", launchToolConfig{}, err
	}
	for _, item := range configured {
		if len(item.Params) == 0 || item.Label == primary.Label {
			continue
		}
		return "", launchToolConfig{}, fmt.Errorf("%s.params is only supported on the primary tool", item.Label)
	}
	return primaryName, *primary, nil
}

func configuredParamsForPrimary(registry *tools.Registry, configured []launchToolConfig, primary string) ([]string, error) {
	if strings.TrimSpace(primary) == "" {
		return nil, nil
	}
	var params []string
	for _, item := range configured {
		toolName, err := resolveConfiguredTool(registry, item.Name)
		if err != nil {
			return nil, err
		}
		if toolName == primary {
			params = item.Params
			continue
		}
		if len(item.Params) > 0 {
			return nil, fmt.Errorf("%s.params is only supported on the primary tool", item.Label)
		}
	}
	return params, nil
}

func resolveConfiguredToolMountProfiles(registry *tools.Registry, configured []launchToolConfig) (map[string]string, error) {
	profiles := map[string]string{}
	for _, item := range configured {
		if strings.TrimSpace(item.MountProfile) == "" {
			continue
		}
		toolName, err := resolveConfiguredTool(registry, item.Name)
		if err != nil {
			return nil, err
		}
		profiles[toolName] = strings.TrimSpace(item.MountProfile)
	}
	if len(profiles) == 0 {
		return nil, nil
	}
	return profiles, nil
}

func resolveConfiguredTool(registry *tools.Registry, name string) (string, error) {
	if _, ok := registry.Get(name); ok {
		return name, nil
	}
	for _, registered := range registry.ToolNames() {
		item := registry.MustGet(registered)
		if item.CommandName() == name {
			return item.Name(), nil
		}
	}
	return "", fmt.Errorf("unknown tool: %s", name)
}

func appendIfMissing(values []string, value string) []string {
	for _, item := range values {
		if item == value {
			return values
		}
	}
	return append(values, value)
}

func launchConfigHome(home string) string {
	if home != "" {
		return home
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return userHome
}

func launchConfigPaths(paths config.Paths) config.Paths {
	paths.Home = launchConfigHome(paths.Home)
	paths.ProjectRoot = strings.TrimSpace(paths.ProjectRoot)
	if paths.ProjectRoot != "" {
		paths.ProjectRoot = config.ExpandHome(paths.ProjectRoot, paths.Home)
		return paths
	}
	if paths.Home == "" {
		paths.ProjectRoot = "Projects"
		return paths
	}
	paths.ProjectRoot = filepath.Join(paths.Home, "Projects")
	return paths
}
