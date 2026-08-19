// Package appconfig loads and resolves Toby's host configuration: it deep-merges
// the config source files, strict-decodes the non-resource schema, and exposes
// resource configuration through an explicit delayed resolution boundary.
package appconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"petris.dev/toby/internal/config"
	configfile "petris.dev/toby/internal/config/file"
	"petris.dev/toby/internal/diagnostic/warning"
	"petris.dev/toby/internal/oci/image"
	"petris.dev/toby/internal/oci/imagesource"
	"petris.dev/toby/internal/permission"
)

const defaultSandboxImage = "mcr.microsoft.com/devcontainers/javascript-node:24-bookworm"
const defaultProfile = "default"

var substitutionPattern = regexp.MustCompile(`\{(env|file):([^}]+)\}`)

type pendingWarning struct {
	id         warning.ID
	message    string
	attributes []any
}

// Service holds the resolved global and launch configuration.
type Service struct {
	Dir             string
	Home            string
	config          Config
	rawResources    map[string]any
	pendingWarnings []pendingWarning
}

// Config is the resolved Toby host configuration.
type Config struct {
	Instructions []string
	Permissions  PermissionConfig
	Settings     SettingsConfig
	Sandbox      SandboxConfig
	Tools        map[string]ToolConfig
}

// SandboxConfig is the resolved OCI source for application sandboxes.
type SandboxConfig struct {
	Source  imagesource.Kind
	Image   string
	Archive string
	Build   imagesource.BuildConfig
	Pull    image.PullPolicy
}

// SettingsConfig contains effective application settings.
type SettingsConfig struct {
	Profile                 string
	SuppressWarnings        warning.Suppression
	AutoloadProjectConfig   *bool
	AllowExternalProjects   *bool
	Debug                   *bool
	Yolo                    *bool
	ManagedTerminal         *bool
	UnknownSuppressWarnings []string
}

// ToolConfig contains host defaults for one registered tool.
type ToolConfig struct {
	Profile string
}

// PermissionConfig contains effective path and action permissions.
type PermissionConfig struct {
	Paths map[string]string
	// Actions maps an action id (its RPC method name, e.g. "git.commit") to the
	// configured rule that governs whether it is allowed, denied, or asked.
	Actions map[string]permission.Rule
}

// ResolvedInstruction is one host instruction source and its detached content.
// Tool adapters choose their own native sandbox destination when they render
// these contents.
type ResolvedInstruction struct {
	Source   string
	Contents []byte
}

// fileSchema is the strict non-resource decode target for a merged config.
// Resource configuration is removed and retained as input-only raw data before
// this strict decode so launches can connect to the agent before resolving it.
type fileSchema struct {
	Instructions []string               `json:"instructions" yaml:"instructions"`
	Sandbox      sandboxSchema          `json:"sandbox" yaml:"sandbox"`
	Permissions  permissionSchema       `json:"permissions" yaml:"permissions"`
	Settings     settingsSchema         `json:"settings" yaml:"settings"`
	Tools        map[string]*toolSchema `json:"tools" yaml:"tools"`
}

type sandboxSchema struct {
	Image   string              `json:"image" yaml:"image"`
	Archive string              `json:"archive" yaml:"archive"`
	Build   *sandboxBuildSchema `json:"build" yaml:"build"`
	Pull    *image.PullPolicy   `json:"pull" yaml:"pull"`
}

type sandboxBuildSchema struct {
	Context    string `json:"context" yaml:"context"`
	Dockerfile string `json:"dockerfile" yaml:"dockerfile"`
}

type permissionSchema struct {
	Paths   map[string]string `json:"paths" yaml:"paths"`
	Actions map[string]string `json:"actions" yaml:"actions"`
}

type settingsSchema struct {
	Profile               string   `json:"profile" yaml:"profile"`
	SuppressWarnings      []string `json:"suppressWarnings" yaml:"suppressWarnings"`
	AutoloadProjectConfig *bool    `json:"autoloadProjectConfig" yaml:"autoloadProjectConfig"`
	AllowExternalProjects *bool    `json:"allowExternalProjects" yaml:"allowExternalProjects"`
	Debug                 *bool    `json:"debug" yaml:"debug"`
	Yolo                  *bool    `json:"yolo" yaml:"yolo"`
	ManagedTerminal       *bool    `json:"managedTerminal" yaml:"managedTerminal"`
}

type toolSchema struct {
	Profile string `json:"profile" yaml:"profile"`
}

// New loads the per-user application configuration.
func New(paths config.Paths) (*Service, error) {
	return Load(paths.TobyConfigDir(), paths.Home)
}

// NewDefaults constructs a Service with canonical host defaults without reading
// configuration files or resolving host substitutions.
func NewDefaults(paths config.Paths) *Service {
	return &Service{
		Dir:    paths.TobyConfigDir(),
		Home:   paths.Home,
		config: emptyConfig(),
	}
}

// Load reads the config source files from dir, deep-merges them as generic maps,
// strict-decodes the result, and resolves it into a Service. The generic merge
// has a later file's value replace an earlier one's; the list-valued instructions
// and settings.suppressWarnings keys are instead unioned across files (first
// occurrence wins on order), so each file contributes additively.
func Load(dir, home string) (*Service, error) {
	merged := map[string]any{}
	var instructions, suppressWarnings []any
	for _, name := range sourceFiles() {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		fileMap := map[string]any{}
		if err := configfile.DecodeFile(path, &fileMap); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if list, ok := fileMap["instructions"].([]any); ok {
			instructions = append(instructions, list...)
		}
		if settings, ok := fileMap["settings"].(map[string]any); ok {
			if list, ok := settings["suppressWarnings"].([]any); ok {
				suppressWarnings = append(suppressWarnings, list...)
			}
		}
		configfile.Merge(merged, fileMap)
	}
	if len(instructions) > 0 {
		merged["instructions"] = dedupeList(instructions)
	}
	if len(suppressWarnings) > 0 {
		settings, ok := merged["settings"].(map[string]any)
		if !ok {
			settings = map[string]any{}
			merged["settings"] = settings
		}
		settings["suppressWarnings"] = dedupeList(suppressWarnings)
	}
	rawResources := map[string]any{}
	if raw, present := merged["resources"]; present {
		var ok bool
		rawResources, ok = raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf(
				"%s: resources must be an object",
				filepath.Join(dir, "config"),
			)
		}
		rawResources = configfile.CloneMap(rawResources)
		delete(merged, "resources")
	}
	var schema fileSchema
	if err := configfile.DecodeInto(merged, &schema); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(dir, "config"), err)
	}
	cfg, pending, err := resolve(schema, dir, home)
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Service{
		Dir:             dir,
		Home:            home,
		config:          cfg,
		rawResources:    rawResources,
		pendingWarnings: pending,
	}, nil
}

func sourceFiles() []string {
	return []string{"config.json", "config.yaml", "config.yml"}
}

// dedupeList returns items with duplicate string entries removed, preserving the
// order of first occurrence. Non-string entries are kept as-is for the strict
// decoder to reject.
func dedupeList(items []any) []any {
	seen := make(map[string]bool, len(items))
	out := make([]any, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			if seen[s] {
				continue
			}
			seen[s] = true
		}
		out = append(out, item)
	}
	return out
}

func emptyConfig() Config {
	return Config{
		Permissions: PermissionConfig{Paths: map[string]string{}, Actions: map[string]permission.Rule{}},
		Settings:    SettingsConfig{Profile: defaultProfile},
		Sandbox:     defaultSandboxConfig(),
		Tools:       map[string]ToolConfig{},
	}
}

func defaultSandboxConfig() SandboxConfig {
	return SandboxConfig{
		Source: imagesource.Registry,
		Image:  defaultSandboxImage,
		Pull:   image.PullIfMissing,
	}
}

// resolve turns the decoded schema into a resolved Config: it expands paths and
// host-side substitutions, parses warning suppression, and clones open
// passthrough bodies so Config never shares mutable structure with decoded maps.
func resolve(schema fileSchema, dir, home string) (Config, []pendingWarning, error) {
	result := emptyConfig()
	result.Instructions = append([]string(nil), schema.Instructions...)
	var pending []pendingWarning

	for pattern, mode := range schema.Permissions.Paths {
		mode = strings.TrimSpace(mode)
		if mode != "allow" && mode != "deny" {
			pending = append(pending, pendingWarning{
				id: warning.PermissionPathInvalid,
				message: fmt.Sprintf(
					"permissions.paths %q has invalid mode %q; skipping it",
					pattern,
					mode,
				),
				attributes: []any{"path", pattern, "mode", mode},
			})
			continue
		}
		result.Permissions.Paths[config.ExpandHome(pattern, home)] = mode
	}

	for action, value := range schema.Permissions.Actions {
		rule, err := permission.ParseRule(value)
		if err != nil {
			return Config{}, nil, fmt.Errorf("permissions.actions.%s: %w", action, err)
		}
		result.Permissions.Actions[action] = rule
	}

	settings := schema.Settings.resolve()
	result.Settings = settings
	for _, id := range settings.UnknownSuppressWarnings {
		pending = append(pending, pendingWarning{
			id: warning.ConfigUnknownWarningID,
			message: fmt.Sprintf(
				"settings.suppressWarnings includes unknown id %q; ignoring it",
				id,
			),
			attributes: []any{"configured_id", id},
		})
	}
	for name, raw := range schema.Tools {
		name = strings.TrimSpace(name)
		if name == "" {
			return Config{}, nil, fmt.Errorf("tools key must not be empty")
		}
		if raw == nil {
			continue
		}
		profile := strings.TrimSpace(raw.Profile)
		if profile != "" {
			result.Tools[name] = ToolConfig{Profile: profile}
		}
	}

	sandbox, err := ResolveSandboxSource(
		SandboxSource{
			Image:   schema.Sandbox.Image,
			Archive: schema.Sandbox.Archive,
			Build:   sandboxBuildFromSchema(schema.Sandbox.Build),
			Pull:    schema.Sandbox.Pull,
		},
		dir,
		home,
		result.Sandbox,
	)
	if err != nil {
		return Config{}, nil, err
	}
	result.Sandbox = sandbox

	return result, pending, nil
}

func (s settingsSchema) resolve() SettingsConfig {
	cfg := SettingsConfig{Profile: strings.TrimSpace(s.Profile)}
	if cfg.Profile == "" {
		cfg.Profile = defaultProfile
	}
	if s.SuppressWarnings != nil {
		suppression, unknown := warning.SuppressionFromList(s.SuppressWarnings)
		cfg.SuppressWarnings = suppression
		cfg.UnknownSuppressWarnings = append([]string(nil), unknown...)
	}
	if s.AutoloadProjectConfig != nil {
		autoload := *s.AutoloadProjectConfig
		cfg.AutoloadProjectConfig = &autoload
	}
	if s.AllowExternalProjects != nil {
		allow := *s.AllowExternalProjects
		cfg.AllowExternalProjects = &allow
	}
	if s.Debug != nil {
		debug := *s.Debug
		cfg.Debug = &debug
	}
	if s.Yolo != nil {
		yolo := *s.Yolo
		cfg.Yolo = &yolo
	}
	if s.ManagedTerminal != nil {
		managed := *s.ManagedTerminal
		cfg.ManagedTerminal = &managed
	}
	return cfg
}

// Validate verifies the effective application configuration.
func (c Config) Validate() error {
	return c.Sandbox.Validate()
}

// Validate checks one effective sandbox source.
func (c SandboxConfig) Validate() error {
	switch c.Pull {
	case image.PullIfMissing, image.PullAlways, image.PullNever:
	default:
		return fmt.Errorf(
			"sandbox.pull has unsupported value %q",
			c.Pull,
		)
	}

	switch c.Source {
	case imagesource.Registry:
		if strings.TrimSpace(c.Image) == "" {
			return fmt.Errorf("sandbox.image must not be empty")
		}
		if c.Archive != "" ||
			c.Build != (imagesource.BuildConfig{}) {
			return fmt.Errorf(
				"registry sandbox source must not contain archive or build configuration",
			)
		}
	case imagesource.Archive:
		if c.Archive == "" {
			return fmt.Errorf("sandbox.archive must not be empty")
		}
		if c.Image != "" ||
			c.Build != (imagesource.BuildConfig{}) {
			return fmt.Errorf(
				"archive sandbox source must not contain image or build configuration",
			)
		}
	case imagesource.Build:
		if c.Build.Context == "" || c.Build.Dockerfile == "" {
			return fmt.Errorf(
				"sandbox.build context and Dockerfile must not be empty",
			)
		}
		if c.Image != "" || c.Archive != "" {
			return fmt.Errorf(
				"build sandbox source must not contain image or archive configuration",
			)
		}
	default:
		return fmt.Errorf(
			"sandbox source %q is unsupported",
			c.Source,
		)
	}
	return nil
}

// SandboxSource is one decoded image, archive, or build selection.
type SandboxSource struct {
	Image   string
	Archive string
	Build   *SandboxBuild
	Pull    *image.PullPolicy
}

// SandboxBuild is one decoded Dockerfile build selection.
type SandboxBuild struct {
	Context    string
	Dockerfile string
}

func sandboxBuildFromSchema(schema *sandboxBuildSchema) *SandboxBuild {
	if schema == nil {
		return nil
	}
	return &SandboxBuild{
		Context:    schema.Context,
		Dockerfile: schema.Dockerfile,
	}
}

// ResolveSandboxSource resolves one image, archive, or build selection relative
// to dir and home. Empty input returns base unchanged except for an explicit pull
// override.
func ResolveSandboxSource(
	source SandboxSource,
	dir string,
	home string,
	base SandboxConfig,
) (SandboxConfig, error) {
	imageValue := strings.TrimSpace(source.Image)
	archiveValue := strings.TrimSpace(source.Archive)
	sources := 0
	if imageValue != "" {
		sources++
	}
	if archiveValue != "" {
		sources++
	}
	if source.Build != nil {
		sources++
	}
	if sources > 1 {
		return SandboxConfig{}, fmt.Errorf(
			"sandbox.image, sandbox.archive, and sandbox.build are mutually exclusive",
		)
	}

	result := base
	switch {
	case imageValue != "":
		result = SandboxConfig{
			Source: imagesource.Registry,
			Image:  imageValue,
			Pull:   image.PullIfMissing,
		}
	case archiveValue != "":
		archivePath, err := resolveSandboxPath(
			archiveValue,
			dir,
			home,
		)
		if err != nil {
			return SandboxConfig{}, fmt.Errorf(
				"sandbox.archive: %w",
				err,
			)
		}
		result = SandboxConfig{
			Source:  imagesource.Archive,
			Archive: archivePath,
			Pull:    image.PullIfMissing,
		}
	case source.Build != nil:
		build, err := resolveSandboxBuild(
			sandboxBuildSchema{
				Context:    source.Build.Context,
				Dockerfile: source.Build.Dockerfile,
			},
			dir,
			home,
		)
		if err != nil {
			return SandboxConfig{}, err
		}
		result = SandboxConfig{
			Source: imagesource.Build,
			Build:  build,
			Pull:   image.PullIfMissing,
		}
	}

	if source.Pull != nil {
		result.Pull = *source.Pull
	}

	return result, nil
}

func resolveSandboxBuild(
	schema sandboxBuildSchema,
	dir string,
	home string,
) (imagesource.BuildConfig, error) {
	contextValue := strings.TrimSpace(schema.Context)
	if contextValue == "" {
		contextValue = "."
	}
	contextPath, err := resolveSandboxPath(contextValue, dir, home)
	if err != nil {
		return imagesource.BuildConfig{}, fmt.Errorf(
			"sandbox.build.context: %w",
			err,
		)
	}

	dockerfileValue := strings.TrimSpace(schema.Dockerfile)
	if dockerfileValue == "" {
		dockerfileValue = "Dockerfile"
	}
	dockerfileValue = config.ExpandHome(dockerfileValue, home)
	if !filepath.IsAbs(dockerfileValue) {
		dockerfileValue = filepath.Join(contextPath, dockerfileValue)
	}
	dockerfilePath, err := filepath.Abs(dockerfileValue)
	if err != nil {
		return imagesource.BuildConfig{}, fmt.Errorf(
			"sandbox.build.dockerfile: %w",
			err,
		)
	}

	return imagesource.BuildConfig{
		Context:    contextPath,
		Dockerfile: filepath.Clean(dockerfilePath),
	}, nil
}

func resolveSandboxPath(value string, dir string, home string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("path must not be empty")
	}
	value = config.ExpandHome(value, home)
	if !filepath.IsAbs(value) {
		value = filepath.Join(dir, value)
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absolute), nil
}

// AutoloadProjectConfigEnabled reports whether project configuration is loaded automatically.
func (c SettingsConfig) AutoloadProjectConfigEnabled() bool {
	return c.AutoloadProjectConfig != nil && *c.AutoloadProjectConfig
}

// AllowExternalProjectsEnabled reports whether projects may reside outside the project root.
func (c SettingsConfig) AllowExternalProjectsEnabled() bool {
	return c.AllowExternalProjects != nil && *c.AllowExternalProjects
}

// DebugEnabled reports whether debug output is enabled.
func (c SettingsConfig) DebugEnabled() bool {
	return c.Debug != nil && *c.Debug
}

// YoloEnabled reports whether permission prompts are bypassed.
func (c SettingsConfig) YoloEnabled() bool {
	return c.Yolo != nil && *c.Yolo
}

// ManagedTerminalEnabled reports whether Toby interposes its managed terminal for the
// interactive foreground tool (raw-passthrough shadow plus the approval modal). It
// defaults to on; only an explicit `settings.managedTerminal: false` (or
// --managed-terminal=false) turns it off, falling back to a plain passthrough — which
// means approval prompts cannot be shown, so anything not explicitly allowed is denied.
func (c SettingsConfig) ManagedTerminalEnabled() bool {
	return c.ManagedTerminal == nil || *c.ManagedTerminal
}

func (s *Service) resolutionContext() ([]string, string) {
	configDirs := []string{}
	if s != nil && s.Dir != "" {
		configDirs = append(configDirs, s.Dir)
	}
	home := ""
	if s != nil {
		home = s.Home
	}
	if home == "" {
		if detected, err := os.UserHomeDir(); err == nil {
			home = detected
		}
	}
	return configDirs, home
}

// defaultPermissionMode is the mode applied to the paths Toby injects by default.
const defaultPermissionMode = "allow"

// defaultPermissionPaths returns the permission paths Toby injects into
// supported tool configs by default. /tmp is available unless the user's
// same-path rule overrides it. When yolo is enabled the filesystem root is
// granted so the tool may reach any path.
func defaultPermissionPaths(yolo bool) map[string]string {
	result := map[string]string{
		"/tmp": defaultPermissionMode,
	}
	if yolo {
		result["/"] = defaultPermissionMode
	}
	return result
}

// PermissionPaths returns Toby's temporary-directory default merged with
// user-configured permission.paths. User entries override the default. When
// yolo is enabled, the filesystem root is granted.
func (s *Service) PermissionPaths() map[string]string {
	result := defaultPermissionPaths(s.Settings().YoloEnabled())
	if s == nil {
		return result
	}
	for pattern, mode := range s.config.Permissions.Paths {
		result[pattern] = mode
	}
	return result
}

// Settings returns a detached copy of the effective application settings.
func (s *Service) Settings() SettingsConfig {
	if s == nil {
		return SettingsConfig{}
	}
	return s.config.Settings
}

// Profile returns the effective home and default tool-volume profile.
func (s *Service) Profile() string {
	profile := strings.TrimSpace(s.Settings().Profile)
	if profile == "" {
		return defaultProfile
	}
	return profile
}

// ToolProfiles returns detached per-tool profile overrides.
func (s *Service) ToolProfiles() map[string]string {
	if s == nil {
		return nil
	}
	result := make(map[string]string)
	for name, tool := range s.config.Tools {
		if profile := strings.TrimSpace(tool.Profile); profile != "" {
			result[name] = profile
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// Sandbox returns the resolved application sandbox image selection.
func (s *Service) Sandbox() SandboxConfig {
	if s == nil {
		return defaultSandboxConfig()
	}
	return s.config.Sandbox
}

// PermissionRule returns the configured rule for an action (by its method-name id,
// e.g. "git.commit"), or RuleUnset when nothing is configured.
func (s *Service) PermissionRule(action string) permission.Rule {
	if s == nil {
		return permission.RuleUnset
	}
	return s.config.Permissions.Actions[action]
}

// LaunchOverrides carries the config-corresponding values a single launch may
// override (from CLI flags and the launch-config file). Folding these into the
// config via WithOverrides keeps the Service the single source of truth.
type LaunchOverrides struct {
	Profile          string
	ToolProfiles     map[string]string
	Sandbox          *SandboxConfig
	Image            string
	Pull             image.PullPolicy
	Debug            *bool
	Yolo             *bool
	ManagedTerminal  *bool
	SuppressWarnings warning.Suppression
}

// WithOverrides returns a new Service whose config has the launch overrides folded
// in. SuppressWarnings merges over the config base; all other set fields replace
// it. The receiver is not mutated, so the process-wide singleton is unaffected.
func (s *Service) WithOverrides(o LaunchOverrides) *Service {
	if s == nil {
		return nil
	}
	next := *s

	settings := s.config.Settings
	settings.SuppressWarnings = s.config.Settings.SuppressWarnings.Clone()
	settings.SuppressWarnings.Merge(o.SuppressWarnings)
	if profile := strings.TrimSpace(o.Profile); profile != "" {
		settings.Profile = profile
	}
	if o.Debug != nil {
		debug := *o.Debug
		settings.Debug = &debug
	}
	if o.Yolo != nil {
		yolo := *o.Yolo
		settings.Yolo = &yolo
	}
	if o.ManagedTerminal != nil {
		managed := *o.ManagedTerminal
		settings.ManagedTerminal = &managed
	}
	next.config.Settings = settings

	tools := make(map[string]ToolConfig, len(s.config.Tools)+len(o.ToolProfiles))
	for name, tool := range s.config.Tools {
		tools[name] = tool
	}
	for name, profile := range o.ToolProfiles {
		if profile = strings.TrimSpace(profile); profile != "" {
			tools[name] = ToolConfig{Profile: profile}
		}
	}
	next.config.Tools = tools

	sandbox := s.config.Sandbox
	if o.Sandbox != nil {
		sandbox = *o.Sandbox
	}
	if o.Image != "" {
		pull := image.PullIfMissing
		if sandbox.Source == imagesource.Registry {
			pull = sandbox.Pull
		}
		sandbox = SandboxConfig{
			Source: imagesource.Registry,
			Image:  o.Image,
			Pull:   pull,
		}
	}
	if o.Pull != "" {
		sandbox.Pull = o.Pull
	}
	next.config.Sandbox = sandbox
	next.pendingWarnings = append([]pendingWarning(nil), s.pendingWarnings...)

	return &next
}

// EmitPendingWarnings reports non-fatal host-configuration issues collected
// during load.
func (s *Service) EmitPendingWarnings(warnings *warning.Service) {
	if s == nil || warnings == nil {
		return
	}
	for _, item := range s.pendingWarnings {
		warnings.Warn(item.id, item.message, item.attributes...)
	}
}

// ResolveInstructions reads configured instruction sources on the host. It does
// not invent a shared sandbox path: each selected tool writes the contents to
// its own native instruction path.
func (s *Service) ResolveInstructions(
	warnings *warning.Service,
) ([]ResolvedInstruction, error) {
	if s == nil {
		return nil, nil
	}
	hostPaths, err := s.instructionHostPaths(warnings)
	if err != nil {
		return nil, err
	}

	resolved := make([]ResolvedInstruction, 0, len(hostPaths))
	for _, hostPath := range hostPaths {
		data, err := os.ReadFile(hostPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				warnInstructionMissing(warnings, hostPath)
				continue
			}
			return nil, fmt.Errorf(
				"read instruction file %s: %w",
				hostPath,
				err,
			)
		}
		resolved = append(resolved, ResolvedInstruction{
			Source:   hostPath,
			Contents: append([]byte(nil), data...),
		})
	}
	return resolved, nil
}

func resolveString(value string, configDirs []string, home string) (string, error) {
	var firstErr error
	resolved := substitutionPattern.ReplaceAllStringFunc(value, func(match string) string {
		if firstErr != nil {
			return ""
		}
		parts := substitutionPattern.FindStringSubmatch(match)
		kind := parts[1]
		target := strings.TrimSpace(parts[2])
		if kind == "env" {
			return os.Getenv(target)
		}
		path := config.ExpandHome(target, home)
		data, err := readSubstitutionFile(path, configDirs)
		if err != nil {
			firstErr = fmt.Errorf("unable to read file substitution %q: %w", target, err)
			return ""
		}
		return strings.TrimSpace(string(data))
	})
	if firstErr != nil {
		return "", firstErr
	}
	return resolved, nil
}

func readSubstitutionFile(path string, configDirs []string) ([]byte, error) {
	if filepath.IsAbs(path) {
		return os.ReadFile(path)
	}
	var firstErr error
	for _, dir := range configDirs {
		if dir == "" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, path))
		if err == nil {
			return data, nil
		}
		if firstErr == nil {
			firstErr = err
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return os.ReadFile(path)
}

func (s *Service) instructionHostPaths(
	warnings *warning.Service,
) ([]string, error) {
	paths := make([]string, 0, len(s.config.Instructions))
	seen := map[string]bool{}
	for _, pattern := range s.config.Instructions {
		matches, err := s.resolveInstructionPattern(pattern, warnings)
		if err != nil {
			return nil, err
		}
		for _, match := range matches {
			if seen[match] {
				continue
			}
			seen[match] = true
			paths = append(paths, match)
		}
	}
	return paths, nil
}

func (s *Service) resolveInstructionPattern(
	pattern string,
	warnings *warning.Service,
) ([]string, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil, nil
	}
	path := config.ExpandHome(pattern, s.Home)
	if !filepath.IsAbs(path) {
		path = filepath.Join(s.Dir, path)
	}
	if hasGlobMeta(path) {
		matches, err := filepath.Glob(path)
		if err != nil {
			return nil, fmt.Errorf("invalid instruction pattern %q: %w", pattern, err)
		}
		if len(matches) == 0 {
			warnInstructionMissing(warnings, pattern)
			return nil, nil
		}
		sort.Strings(matches)
		return cleanInstructionPaths(matches)
	}
	return cleanInstructionPaths([]string{path})
}

func warnInstructionMissing(warnings *warning.Service, source string) {
	if warnings == nil {
		return
	}
	warnings.Warn(
		warning.ConfigInstructionMissing,
		fmt.Sprintf("instruction source %q is missing; skipping it", source),
		"source", source,
	)
}

func cleanInstructionPaths(paths []string) ([]string, error) {
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		result = append(result, abs)
	}
	return result, nil
}

func hasGlobMeta(path string) bool {
	return strings.ContainsAny(path, "*?[")
}
