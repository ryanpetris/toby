package launchconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/config/file"
	"petris.dev/toby/internal/config/toby"
	"petris.dev/toby/internal/diagnostic/exitcode"
	"petris.dev/toby/internal/diagnostic/warning"
	sandboxmount "petris.dev/toby/internal/sandbox/mount"
	"petris.dev/toby/internal/tools/helpers"
	"petris.dev/toby/internal/tools/tool"
)

const projectLaunchConfigName = ".toby.yaml"

type Params struct {
	Registry *tool.Registry
	Paths    config.Paths
	Config   *tobyconfig.Service
	Stderr   io.Writer
}

type DirectLaunch struct {
	Options        tool.CommandOptions
	Extra          []string
	RequestedTools []string
}

type ConfiguredLaunch struct {
	Options        tool.CommandOptions
	Extra          []string
	RequestedTools []string
	Primary        string
}

type launchConfig struct {
	MountProfiles sandboxmount.Profiles
	Settings      launchSettingsConfig
	Sandbox       launchSandboxConfig
	Projects      []launchProjectConfig
	Workdir       string
	Tools         []launchToolConfig
}

type launchSandboxConfig struct {
	Name    string
	Runtime launchRuntimeConfig
}

type launchSettingsConfig struct {
	MountProfile     string
	AutoUpgrade      bool
	SuppressWarnings warning.Suppression
	Debug            *bool
	Yolo             *bool
}

type launchRuntimeConfig struct {
	Default    string
	Docker     launchDockerConfig
	Bubblewrap launchBubblewrapConfig
}

type launchDockerConfig struct {
	Image    string
	Home     string
	Projects string
	Build    tool.DockerBuildConfig
}

type launchBubblewrapConfig struct {
	Root string
}

type launchToolConfig struct {
	Name         string
	Label        string
	MountProfile string
	Params       []string
	Primary      bool
}

type launchProjectConfig struct {
	Mount   tool.ProjectMount
	Label   string
	Primary bool
}

type rawLaunchConfig struct {
	MountProfiles map[string]*rawLaunchMountProfile `yaml:"mountProfiles" json:"mountProfiles"`
	Settings      rawLaunchSettingsConfig           `yaml:"settings" json:"settings"`
	Sandbox       rawLaunchSandboxConfig            `yaml:"sandbox" json:"sandbox"`
	Projects      map[string]*rawLaunchProject      `yaml:"projects" json:"projects"`
	Workdir       string                            `yaml:"workdir" json:"workdir"`
	Tools         map[string]*rawLaunchTool         `yaml:"tools" json:"tools"`
}

type rawLaunchMountProfile struct {
	Backing  string `yaml:"backing" json:"backing"`
	HostRoot string `yaml:"hostRoot" json:"hostRoot"`
}

type rawLaunchSettingsConfig struct {
	MountProfile     string               `yaml:"mountProfile" json:"mountProfile"`
	AutoUpgrade      bool                 `yaml:"autoUpgrade" json:"autoUpgrade"`
	SuppressWarnings rawLaunchSuppression `yaml:"suppressWarnings" json:"suppressWarnings"`
	Debug            *bool                `yaml:"debug" json:"debug"`
	Yolo             *bool                `yaml:"yolo" json:"yolo"`
}

type rawLaunchSandboxConfig struct {
	Name    string                 `yaml:"name" json:"name"`
	Runtime rawLaunchRuntimeConfig `yaml:"runtime" json:"runtime"`
}

type rawLaunchSandboxFields struct {
	Name    string                 `yaml:"name" json:"name"`
	Runtime rawLaunchRuntimeConfig `yaml:"runtime" json:"runtime"`
}

type rawLaunchRuntimeConfig struct {
	Default    string
	Docker     rawLaunchDockerConfig
	Bubblewrap rawLaunchBubblewrapConfig
}

type rawLaunchRuntimeFields struct {
	Default    string                    `yaml:"default" json:"default"`
	Docker     rawLaunchDockerConfig     `yaml:"docker" json:"docker"`
	Bubblewrap rawLaunchBubblewrapConfig `yaml:"bubblewrap" json:"bubblewrap"`
}

type rawLaunchDockerConfig struct {
	Image    string                `yaml:"image" json:"image"`
	Home     string                `yaml:"home" json:"home"`
	Projects string                `yaml:"projects" json:"projects"`
	Build    *rawLaunchDockerBuild `yaml:"build" json:"build"`
}

type rawLaunchDockerBuild struct {
	Context    rawLaunchString `yaml:"context" json:"context"`
	Dockerfile rawLaunchString `yaml:"dockerfile" json:"dockerfile"`
}

type rawLaunchBubblewrapConfig struct {
	Root string `yaml:"root" json:"root"`
}

type rawLaunchProject struct {
	Path    rawLaunchString `yaml:"path" json:"path"`
	Primary bool            `yaml:"primary" json:"primary"`
}

type rawLaunchTool struct {
	MountProfile string   `yaml:"mountProfile" json:"mountProfile"`
	Params       []string `yaml:"params" json:"params"`
	Primary      bool     `yaml:"primary" json:"primary"`
}

type rawLaunchSuppression struct {
	Set   bool
	Value any
}

type rawLaunchString struct {
	Set   bool
	Value string
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
	tools, err := resolveConfiguredTools(params.Registry, cfg.Tools)
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
	profiles, err := resolveConfiguredToolMountProfiles(params.Registry, cfg.Tools)
	if err != nil {
		return ConfiguredLaunch{}, err
	}
	options.ToolMountProfiles = profiles
	return ConfiguredLaunch{
		Options:        options,
		Extra:          configuredLaunchExtra(primaryToolConfig.Params, extra),
		RequestedTools: tools,
		Primary:        primaryTool,
	}, nil
}

func BuildOverlayConfiguredLaunch(params Params, configPath string, parsed DirectLaunch, primary string, primaryProject tool.ProjectMount) (ConfiguredLaunch, error) {
	cfg, err := loadLaunchConfigWithPaths(configPath, params.Paths)
	if err != nil {
		return ConfiguredLaunch{}, err
	}
	tools, err := resolveConfiguredTools(params.Registry, cfg.Tools)
	if err != nil {
		return ConfiguredLaunch{}, err
	}
	primaryParams, err := configuredParamsForPrimary(params.Registry, cfg.Tools, primary)
	if err != nil {
		return ConfiguredLaunch{}, err
	}
	options := commandOptionsFromLaunchConfig(cfg)
	profiles, err := resolveConfiguredToolMountProfiles(params.Registry, cfg.Tools)
	if err != nil {
		return ConfiguredLaunch{}, err
	}
	options.ToolMountProfiles = profiles
	if options.Env == "" {
		options.Env = parsed.Options.Env
	}
	options.Install = parsed.Options.Install
	options.Upgrade = options.Upgrade || parsed.Options.Upgrade
	options.Projects = append([]tool.ProjectMount{primaryProject}, options.Projects...)
	mergeDirectLaunchOptions(&options, parsed.Options)
	requestedTools := appendIfMissing(nil, primary)
	for _, name := range parsed.RequestedTools {
		requestedTools = appendIfMissing(requestedTools, name)
	}
	for _, name := range tools {
		requestedTools = appendIfMissing(requestedTools, name)
	}
	return ConfiguredLaunch{
		Options:        options,
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

func commandOptionsFromLaunchConfig(cfg launchConfig) tool.CommandOptions {
	return tool.CommandOptions{
		Env:              cfg.Sandbox.Name,
		Upgrade:          cfg.Settings.AutoUpgrade,
		Projects:         projectMounts(cfg.Projects),
		Workdir:          cfg.Workdir,
		SandboxRuntime:   cfg.Sandbox.Runtime.Default,
		DockerImage:      cfg.Sandbox.Runtime.Docker.Image,
		DockerHome:       cfg.Sandbox.Runtime.Docker.Home,
		DockerProjects:   cfg.Sandbox.Runtime.Docker.Projects,
		DockerBuild:      cfg.Sandbox.Runtime.Docker.Build,
		BubblewrapRoot:   cfg.Sandbox.Runtime.Bubblewrap.Root,
		MountProfile:     cfg.Settings.MountProfile,
		MountProfiles:    cfg.MountProfiles,
		SuppressWarnings: cfg.Settings.SuppressWarnings,
		Debug:            cloneBool(cfg.Settings.Debug),
		Yolo:             cloneBool(cfg.Settings.Yolo),
	}
}

func mergeDirectLaunchOptions(dst *tool.CommandOptions, src tool.CommandOptions) {
	if src.SandboxRuntime != "" {
		dst.SandboxRuntime = src.SandboxRuntime
	}
	if src.DockerImage != "" {
		dst.DockerImage = src.DockerImage
	}
	if src.DockerHome != "" {
		dst.DockerHome = src.DockerHome
	}
	if src.DockerProjects != "" {
		dst.DockerProjects = src.DockerProjects
	}
	if src.DockerBuild.IsSet() {
		dst.DockerBuild = src.DockerBuild
	}
	dst.MountProfiles.Merge(src.MountProfiles)
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

func loadLaunchConfig(path, home string) (launchConfig, error) {
	return loadLaunchConfigWithPaths(path, config.Paths{Home: home})
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
	var raw rawLaunchConfig
	if err := configfile.DecodeInto(data, configfile.FormatYAML, "launch config", &raw); err != nil {
		return launchConfig{}, fmt.Errorf("%s: %w", abs, err)
	}
	cfg, err := parseLaunchConfigWithPaths(raw, filepath.Dir(abs), paths)
	if err != nil {
		return launchConfig{}, fmt.Errorf("%s: %w", abs, err)
	}
	return cfg, nil
}

func parseLaunchConfig(raw rawLaunchConfig, dir, home string) (launchConfig, error) {
	return parseLaunchConfigWithPaths(raw, dir, config.Paths{Home: home})
}

func parseLaunchConfigWithPaths(raw rawLaunchConfig, dir string, paths config.Paths) (launchConfig, error) {
	paths = launchConfigPaths(paths)
	var cfg launchConfig
	profiles, err := rawLaunchMountProfiles(raw.MountProfiles, dir, paths.Home)
	if err != nil {
		return launchConfig{}, err
	}
	cfg.MountProfiles = profiles
	settings, err := raw.Settings.toSettings()
	if err != nil {
		return launchConfig{}, err
	}
	cfg.Settings = settings
	sandbox, err := raw.Sandbox.toSandbox(dir, paths.Home)
	if err != nil {
		return launchConfig{}, err
	}
	cfg.Sandbox = sandbox
	projects, err := rawLaunchProjects(raw.Projects, dir, paths)
	if err != nil {
		return launchConfig{}, err
	}
	cfg.Projects = projects
	cfg.Workdir = raw.Workdir
	tools, err := rawLaunchTools(raw.Tools)
	if err != nil {
		return launchConfig{}, err
	}
	cfg.Tools = tools
	return cfg, nil
}

func rawLaunchMountProfiles(raw map[string]*rawLaunchMountProfile, dir, home string) (sandboxmount.Profiles, error) {
	profiles := sandboxmount.Profiles{}
	for rawName, rawProfile := range raw {
		name := strings.TrimSpace(rawName)
		if name == "" {
			return nil, fmt.Errorf("mountProfiles keys must not be empty")
		}
		if rawProfile == nil {
			return nil, fmt.Errorf("mountProfiles.%s must be an object", name)
		}
		profile, err := rawProfile.toMountProfile("mountProfiles."+name, dir, home)
		if err != nil {
			return nil, err
		}
		profiles[name] = profile
	}
	return profiles, nil
}

func (r rawLaunchMountProfile) toMountProfile(label string, dir, home string) (sandboxmount.BackingConfig, error) {
	var cfg sandboxmount.BackingConfig
	if r.Backing != "" {
		backing, err := helpers.ParseMountBacking(r.Backing)
		if err != nil {
			return sandboxmount.BackingConfig{}, fmt.Errorf("%s.backing: %w", label, err)
		}
		cfg.Backing = backing
	}
	if r.HostRoot != "" {
		root, err := helpers.ResolveMountHostRoot(r.HostRoot, home, dir)
		if err != nil {
			return sandboxmount.BackingConfig{}, fmt.Errorf("%s.hostRoot: %w", label, err)
		}
		cfg.HostRoot = root
	}
	return cfg, nil
}

func (r rawLaunchSettingsConfig) toSettings() (launchSettingsConfig, error) {
	cfg := launchSettingsConfig{MountProfile: strings.TrimSpace(r.MountProfile), AutoUpgrade: r.AutoUpgrade, Debug: cloneBool(r.Debug), Yolo: cloneBool(r.Yolo)}
	if r.SuppressWarnings.Set {
		suppression, err := warning.ParseSuppression(r.SuppressWarnings.Value, "settings.suppressWarnings")
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

func (r rawLaunchSandboxConfig) toSandbox(dir, home string) (launchSandboxConfig, error) {
	runtime, err := r.Runtime.toRuntime(dir, home)
	if err != nil {
		return launchSandboxConfig{}, err
	}
	return launchSandboxConfig{Name: strings.TrimSpace(r.Name), Runtime: runtime}, nil
}

func (r rawLaunchRuntimeConfig) toRuntime(dir, home string) (launchRuntimeConfig, error) {
	docker, err := r.Docker.toDocker(dir, home)
	if err != nil {
		return launchRuntimeConfig{}, err
	}
	bubblewrap, err := r.Bubblewrap.toBubblewrap(dir, home)
	if err != nil {
		return launchRuntimeConfig{}, err
	}
	return launchRuntimeConfig{Default: strings.TrimSpace(r.Default), Docker: docker, Bubblewrap: bubblewrap}, nil
}

func (r rawLaunchDockerConfig) toDocker(dir, home string) (launchDockerConfig, error) {
	cfg := launchDockerConfig{Image: strings.TrimSpace(r.Image), Home: strings.TrimSpace(r.Home), Projects: strings.TrimSpace(r.Projects)}
	if r.Build != nil {
		build, err := r.Build.toDockerBuild(dir, home)
		if err != nil {
			return launchDockerConfig{}, err
		}
		cfg.Build = build
	}
	return cfg, nil
}

func (r rawLaunchDockerBuild) toDockerBuild(dir, home string) (tool.DockerBuildConfig, error) {
	contextValue := "."
	if r.Context.Set {
		contextValue = strings.TrimSpace(r.Context.Value)
		if contextValue == "" {
			return tool.DockerBuildConfig{}, fmt.Errorf("sandbox.runtime.docker.build.context must not be empty")
		}
	}
	dockerfileValue := "Dockerfile"
	if r.Dockerfile.Set {
		dockerfileValue = strings.TrimSpace(r.Dockerfile.Value)
		if dockerfileValue == "" {
			return tool.DockerBuildConfig{}, fmt.Errorf("sandbox.runtime.docker.build.dockerfile must not be empty")
		}
	}
	contextDir, err := resolveLaunchConfigPath(contextValue, dir, home)
	if err != nil {
		return tool.DockerBuildConfig{}, fmt.Errorf("sandbox.runtime.docker.build.context: %w", err)
	}
	dockerfile := config.ExpandHome(dockerfileValue, home)
	if !filepath.IsAbs(dockerfile) {
		dockerfile = filepath.Join(contextDir, dockerfile)
	}
	return tool.DockerBuildConfig{Context: contextDir, Dockerfile: dockerfile}, nil
}

func (r rawLaunchBubblewrapConfig) toBubblewrap(dir, home string) (launchBubblewrapConfig, error) {
	if strings.TrimSpace(r.Root) == "" {
		return launchBubblewrapConfig{}, nil
	}
	root, err := resolveLaunchConfigPath(r.Root, dir, home)
	if err != nil {
		return launchBubblewrapConfig{}, fmt.Errorf("sandbox.runtime.bubblewrap.root: %w", err)
	}
	return launchBubblewrapConfig{Root: root}, nil
}

func rawLaunchProjects(raw map[string]*rawLaunchProject, dir string, paths config.Paths) ([]launchProjectConfig, error) {
	paths = launchConfigPaths(paths)
	projects := make([]launchProjectConfig, 0, len(raw))
	for _, name := range sortedKeys(raw) {
		project, err := raw[name].toProject("projects."+name, name, dir, paths)
		if err != nil {
			return nil, err
		}
		projects = append(projects, project)
	}
	return projects, nil
}

func (r *rawLaunchProject) toProject(label, name string, dir string, paths config.Paths) (launchProjectConfig, error) {
	paths = launchConfigPaths(paths)
	path := ""
	pathSet := false
	primary := false
	if r != nil {
		path = r.Path.Value
		pathSet = r.Path.Set
		primary = r.Primary
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
	return launchProjectConfig{Mount: tool.ProjectMount{Name: name, Source: source}, Label: label, Primary: primary}, nil
}

func rawLaunchTools(raw map[string]*rawLaunchTool) ([]launchToolConfig, error) {
	tools := make([]launchToolConfig, 0, len(raw))
	for _, name := range sortedKeys(raw) {
		parsed, err := raw[name].toTool("tools."+name, name)
		if err != nil {
			return nil, err
		}
		tools = append(tools, parsed)
	}
	return tools, nil
}

func (r *rawLaunchTool) toTool(label, name string) (launchToolConfig, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return launchToolConfig{}, fmt.Errorf("%s key must not be empty", label)
	}
	if r == nil {
		return launchToolConfig{Name: name, Label: label}, nil
	}
	params := append([]string(nil), r.Params...)
	return launchToolConfig{Name: name, Label: label, MountProfile: strings.TrimSpace(r.MountProfile), Params: params, Primary: r.Primary}, nil
}

func (r *rawLaunchRuntimeConfig) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		if value.Tag != "!!str" {
			return fmt.Errorf("sandbox.runtime must be a string or object")
		}
		r.Default = strings.TrimSpace(value.Value)
		return nil
	case yaml.MappingNode:
		var fields rawLaunchRuntimeFields
		if err := decodeLaunchYAMLNodeStrict(value, &fields); err != nil {
			return err
		}
		r.Default = fields.Default
		r.Docker = fields.Docker
		r.Bubblewrap = fields.Bubblewrap
		return nil
	default:
		return fmt.Errorf("sandbox.runtime must be a string or object")
	}
}

func (r *rawLaunchSandboxConfig) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode && value.Tag == "!!null" {
		return nil
	}
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("sandbox must be an object")
	}
	for i := 0; i+1 < len(value.Content); i += 2 {
		key := value.Content[i].Value
		if key != "name" && key != "runtime" {
			return fmt.Errorf("unsupported sandbox key %q", key)
		}
	}
	var fields rawLaunchSandboxFields
	if err := decodeLaunchYAMLNodeStrict(value, &fields); err != nil {
		return err
	}
	r.Name = fields.Name
	r.Runtime = fields.Runtime
	return nil
}

func (r *rawLaunchSandboxConfig) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	var items map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &items); err != nil {
		return fmt.Errorf("sandbox must be an object")
	}
	for key := range items {
		if key != "name" && key != "runtime" {
			return fmt.Errorf("unsupported sandbox key %q", key)
		}
	}
	var fields rawLaunchSandboxFields
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(&fields); err != nil {
		return err
	}
	r.Name = fields.Name
	r.Runtime = fields.Runtime
	return nil
}

func (r *rawLaunchRuntimeConfig) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return fmt.Errorf("sandbox.runtime must be a string or object")
	}
	if trimmed[0] == '"' {
		var value string
		if err := json.Unmarshal(trimmed, &value); err != nil {
			return err
		}
		r.Default = strings.TrimSpace(value)
		return nil
	}
	var fields rawLaunchRuntimeFields
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(&fields); err != nil {
		return err
	}
	r.Default = fields.Default
	r.Docker = fields.Docker
	r.Bubblewrap = fields.Bubblewrap
	return nil
}

func (s *rawLaunchSuppression) UnmarshalYAML(value *yaml.Node) error {
	s.Set = true
	var decoded any
	if err := value.Decode(&decoded); err != nil {
		return err
	}
	s.Value = decoded
	return nil
}

func (s *rawLaunchSuppression) UnmarshalJSON(data []byte) error {
	s.Set = true
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	s.Value = decoded
	return nil
}

func (s *rawLaunchString) UnmarshalYAML(value *yaml.Node) error {
	s.Set = true
	if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
		return fmt.Errorf("must be a string")
	}
	s.Value = value.Value
	return nil
}

func (s *rawLaunchString) UnmarshalJSON(data []byte) error {
	s.Set = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return fmt.Errorf("must be a string")
	}
	return json.Unmarshal(data, &s.Value)
}

func decodeLaunchYAMLNodeStrict(value *yaml.Node, dest any) error {
	data, err := yaml.Marshal(value)
	if err != nil {
		return err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	return decoder.Decode(dest)
}

func resolveLaunchConfigPath(path, dir, home string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("must not be empty")
	}
	path = config.ExpandHome(path, home)
	if filepath.IsAbs(path) {
		return path, nil
	}
	return joinConfigRelativePath(dir, path), nil
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

func ResolveDirectLaunchProject(paths config.Paths, opts tool.CommandOptions) (tool.ProjectMount, error) {
	if strings.TrimSpace(opts.Env) == "" {
		return tool.ProjectMount{}, exitcode.New(2, "environment name is required")
	}
	raw := opts.Project
	if strings.TrimSpace(raw) == "" {
		raw = filepath.Join(paths.ProjectRoot, opts.Env)
	} else {
		raw = config.ExpandHome(raw, paths.Home)
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return tool.ProjectMount{}, err
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return tool.ProjectMount{}, exitcode.New(1, "failed to resolve project directory: %s does not exist", raw)
	}
	if _, err := relativeToProjectRoot(paths.ProjectRoot, abs); err != nil {
		return tool.ProjectMount{}, exitcode.New(1, "project directory must be under %s: %s", paths.ProjectRoot, err)
	}
	name := strings.TrimSpace(filepath.Base(abs))
	if name == "" || name == "." || name == ".." {
		return tool.ProjectMount{}, exitcode.New(2, "invalid project name: %q", name)
	}
	return tool.ProjectMount{Name: name, Source: abs}, nil
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

func projectMounts(projects []launchProjectConfig) []tool.ProjectMount {
	mounts := make([]tool.ProjectMount, 0, len(projects))
	for _, project := range projects {
		mounts = append(mounts, project.Mount)
	}
	return mounts
}

func orderedProjectMounts(projects []launchProjectConfig, primary launchProjectConfig) []tool.ProjectMount {
	mounts := make([]tool.ProjectMount, 0, len(projects))
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

func resolveConfiguredTools(registry *tool.Registry, configured []launchToolConfig) ([]string, error) {
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

func resolvePrimaryConfiguredTool(registry *tool.Registry, configured []launchToolConfig, cliPrimary string) (string, launchToolConfig, error) {
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

func configuredParamsForPrimary(registry *tool.Registry, configured []launchToolConfig, primary string) ([]string, error) {
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

func resolveConfiguredToolMountProfiles(registry *tool.Registry, configured []launchToolConfig) (map[string]string, error) {
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

func resolveConfiguredTool(registry *tool.Registry, name string) (string, error) {
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
