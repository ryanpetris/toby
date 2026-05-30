package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/configfile"
	"petris.dev/toby/internal/exitcode"
	"petris.dev/toby/internal/tool"
	"petris.dev/toby/internal/warning"
)

type launchConfig struct {
	Sandbox  launchSandboxConfig
	Projects []tool.ProjectMount
	Workdir  string
	Tools    []launchToolConfig
}

type launchSandboxConfig struct {
	Name             string
	AutoUpgrade      bool
	Runtime          launchRuntimeConfig
	Tools            tool.ToolStateSettings
	SuppressWarnings warning.Suppression
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
}

type launchBubblewrapConfig struct {
	Root string
}

type launchToolConfig struct {
	Name   string
	Params []string
}

type configuredLaunch struct {
	Options        tool.CommandOptions
	Extra          []string
	RequestedTools []string
	Primary        string
}

func buildConfiguredLaunch(params Params, configPath string, extra []string) (configuredLaunch, error) {
	cfg, err := loadLaunchConfig(configPath, params.Paths.Home)
	if err != nil {
		return configuredLaunch{}, err
	}
	tools, err := resolveConfiguredTools(params.Registry, cfg.Tools)
	if err != nil {
		return configuredLaunch{}, err
	}
	if len(tools) == 0 {
		return configuredLaunch{}, exitcode.New(2, "launch config tools must not be empty")
	}
	return configuredLaunch{
		Options: tool.CommandOptions{
			Env:              cfg.Sandbox.Name,
			Upgrade:          cfg.Sandbox.AutoUpgrade,
			Projects:         cfg.Projects,
			Workdir:          cfg.Workdir,
			SandboxRuntime:   cfg.Sandbox.Runtime.Default,
			DockerImage:      cfg.Sandbox.Runtime.Docker.Image,
			DockerHome:       cfg.Sandbox.Runtime.Docker.Home,
			DockerProjects:   cfg.Sandbox.Runtime.Docker.Projects,
			BubblewrapRoot:   cfg.Sandbox.Runtime.Bubblewrap.Root,
			ToolStates:       cfg.Sandbox.Tools,
			SuppressWarnings: cfg.Sandbox.SuppressWarnings,
		},
		Extra:          configuredLaunchExtra(cfg.Tools[0].Params, extra),
		RequestedTools: tools,
		Primary:        tools[0],
	}, nil
}

func configuredLaunchExtra(params, extra []string) []string {
	result := make([]string, 0, len(params)+len(extra))
	result = append(result, params...)
	result = append(result, extra...)
	return result
}

func loadLaunchConfig(path, home string) (launchConfig, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return launchConfig{}, exitcode.New(2, "--config requires a value")
	}
	home = launchConfigHome(home)
	expanded := config.ExpandHome(path, home)
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return launchConfig{}, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return launchConfig{}, err
	}
	raw, err := configfile.Decode(data, configfile.FormatYAML, "launch config")
	if err != nil {
		return launchConfig{}, fmt.Errorf("%s: %w", abs, err)
	}
	cfg, err := parseLaunchConfig(raw, filepath.Dir(abs), home)
	if err != nil {
		return launchConfig{}, fmt.Errorf("%s: %w", abs, err)
	}
	return cfg, nil
}

func parseLaunchConfig(raw map[string]any, dir, home string) (launchConfig, error) {
	var cfg launchConfig
	for key, value := range raw {
		switch key {
		case "sandbox":
			sandbox, err := parseLaunchSandbox(value, dir, home)
			if err != nil {
				return launchConfig{}, err
			}
			cfg.Sandbox = sandbox
		case "projects":
			projects, err := parseLaunchProjects(value, dir, home)
			if err != nil {
				return launchConfig{}, err
			}
			cfg.Projects = projects
		case "workdir":
			workdir, ok := value.(string)
			if !ok {
				return launchConfig{}, fmt.Errorf("workdir must be a string")
			}
			cfg.Workdir = workdir
		case "tools":
			tools, err := parseLaunchTools(value)
			if err != nil {
				return launchConfig{}, err
			}
			cfg.Tools = tools
		default:
			return launchConfig{}, fmt.Errorf("unsupported top-level key %q", key)
		}
	}
	if len(cfg.Projects) == 0 {
		return launchConfig{}, fmt.Errorf("launch config projects must not be empty")
	}
	if len(cfg.Tools) == 0 {
		return launchConfig{}, fmt.Errorf("launch config tools must not be empty")
	}
	if strings.TrimSpace(cfg.Sandbox.Name) == "" {
		cfg.Sandbox.Name = cfg.Projects[0].Name
	}
	return cfg, nil
}

func parseLaunchSandbox(raw any, dir, home string) (launchSandboxConfig, error) {
	var cfg launchSandboxConfig
	if raw == nil {
		return cfg, nil
	}
	items, ok := raw.(map[string]any)
	if !ok {
		return launchSandboxConfig{}, fmt.Errorf("sandbox must be an object")
	}
	for key, value := range items {
		switch key {
		case "name":
			name, ok := value.(string)
			if !ok {
				return launchSandboxConfig{}, fmt.Errorf("sandbox.name must be a string")
			}
			cfg.Name = strings.TrimSpace(name)
		case "autoUpgrade":
			autoUpgrade, ok := value.(bool)
			if !ok {
				return launchSandboxConfig{}, fmt.Errorf("sandbox.autoUpgrade must be a boolean")
			}
			cfg.AutoUpgrade = autoUpgrade
		case "runtime":
			runtime, err := parseLaunchRuntime(value, dir, home)
			if err != nil {
				return launchSandboxConfig{}, err
			}
			cfg.Runtime = runtime
		case "tools":
			tools, err := parseLaunchSandboxTools(value, dir, home)
			if err != nil {
				return launchSandboxConfig{}, err
			}
			cfg.Tools = tools
		case "suppressWarnings":
			suppression, err := warning.ParseSuppression(value, "sandbox.suppressWarnings")
			if err != nil {
				return launchSandboxConfig{}, err
			}
			cfg.SuppressWarnings = suppression
		default:
			return launchSandboxConfig{}, fmt.Errorf("unsupported sandbox key %q", key)
		}
	}
	return cfg, nil
}

func parseLaunchRuntime(raw any, dir, home string) (launchRuntimeConfig, error) {
	switch value := raw.(type) {
	case string:
		return launchRuntimeConfig{Default: strings.TrimSpace(value)}, nil
	case map[string]any:
		var cfg launchRuntimeConfig
		for key, item := range value {
			switch key {
			case "default":
				name, ok := item.(string)
				if !ok {
					return launchRuntimeConfig{}, fmt.Errorf("sandbox.runtime.default must be a string")
				}
				cfg.Default = strings.TrimSpace(name)
			case "docker":
				docker, err := parseLaunchDocker(item)
				if err != nil {
					return launchRuntimeConfig{}, err
				}
				cfg.Docker = docker
			case "bubblewrap":
				bubblewrap, err := parseLaunchBubblewrap(item, dir, home)
				if err != nil {
					return launchRuntimeConfig{}, err
				}
				cfg.Bubblewrap = bubblewrap
			default:
				return launchRuntimeConfig{}, fmt.Errorf("unsupported sandbox.runtime key %q", key)
			}
		}
		return cfg, nil
	default:
		return launchRuntimeConfig{}, fmt.Errorf("sandbox.runtime must be a string or object")
	}
}

func parseLaunchDocker(raw any) (launchDockerConfig, error) {
	items, ok := raw.(map[string]any)
	if !ok {
		return launchDockerConfig{}, fmt.Errorf("sandbox.runtime.docker must be an object")
	}
	var cfg launchDockerConfig
	for key, value := range items {
		s, ok := value.(string)
		if !ok {
			return launchDockerConfig{}, fmt.Errorf("sandbox.runtime.docker.%s must be a string", key)
		}
		s = strings.TrimSpace(s)
		switch key {
		case "image":
			cfg.Image = s
		case "home":
			cfg.Home = s
		case "projects":
			cfg.Projects = s
		default:
			return launchDockerConfig{}, fmt.Errorf("unsupported sandbox.runtime.docker key %q", key)
		}
	}
	return cfg, nil
}

func parseLaunchSandboxTools(raw any, dir, home string) (tool.ToolStateSettings, error) {
	items, ok := raw.(map[string]any)
	if !ok {
		return tool.ToolStateSettings{}, fmt.Errorf("sandbox.tools must be an object")
	}
	settings := tool.ToolStateSettings{}
	for name, rawTool := range items {
		name = strings.TrimSpace(name)
		if name == "" {
			return tool.ToolStateSettings{}, fmt.Errorf("sandbox.tools keys must not be empty")
		}
		toolConfig, ok := rawTool.(map[string]any)
		if !ok {
			return tool.ToolStateSettings{}, fmt.Errorf("sandbox.tools.%s must be an object", name)
		}
		cfg, err := parseLaunchSandboxTool(name, toolConfig, dir, home)
		if err != nil {
			return tool.ToolStateSettings{}, err
		}
		if cfg.State == "" && cfg.StateRoot == "" {
			continue
		}
		if name == "default" {
			settings.Default = cfg
			continue
		}
		if settings.Tools == nil {
			settings.Tools = map[string]tool.ToolStateConfig{}
		}
		settings.Tools[name] = cfg
	}
	return settings, nil
}

func parseLaunchSandboxTool(name string, raw map[string]any, dir, home string) (tool.ToolStateConfig, error) {
	var cfg tool.ToolStateConfig
	for key, value := range raw {
		switch key {
		case "state":
			rawState, ok := value.(string)
			if !ok {
				return tool.ToolStateConfig{}, fmt.Errorf("sandbox.tools.%s.state must be a string", name)
			}
			parsed, err := tool.ParseToolState(rawState)
			if err != nil {
				return tool.ToolStateConfig{}, fmt.Errorf("sandbox.tools.%s.state: %w", name, err)
			}
			cfg.State = parsed
		case "stateRoot":
			rawRoot, ok := value.(string)
			if !ok {
				return tool.ToolStateConfig{}, fmt.Errorf("sandbox.tools.%s.stateRoot must be a string", name)
			}
			root, err := tool.ResolveStateRoot(rawRoot, home, dir)
			if err != nil {
				return tool.ToolStateConfig{}, fmt.Errorf("sandbox.tools.%s.stateRoot: %w", name, err)
			}
			cfg.StateRoot = root
		default:
			return tool.ToolStateConfig{}, fmt.Errorf("unsupported sandbox.tools.%s key %q", name, key)
		}
	}
	return cfg, nil
}

func parseLaunchBubblewrap(raw any, dir, home string) (launchBubblewrapConfig, error) {
	items, ok := raw.(map[string]any)
	if !ok {
		return launchBubblewrapConfig{}, fmt.Errorf("sandbox.runtime.bubblewrap must be an object")
	}
	var cfg launchBubblewrapConfig
	for key, value := range items {
		s, ok := value.(string)
		if !ok {
			return launchBubblewrapConfig{}, fmt.Errorf("sandbox.runtime.bubblewrap.%s must be a string", key)
		}
		s = strings.TrimSpace(s)
		switch key {
		case "root":
			root, err := resolveLaunchConfigPath(s, dir, home)
			if err != nil {
				return launchBubblewrapConfig{}, fmt.Errorf("sandbox.runtime.bubblewrap.root: %w", err)
			}
			cfg.Root = root
		default:
			return launchBubblewrapConfig{}, fmt.Errorf("unsupported sandbox.runtime.bubblewrap key %q", key)
		}
	}
	return cfg, nil
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

func parseLaunchProjects(raw any, dir, home string) ([]tool.ProjectMount, error) {
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("projects must be an array")
	}
	projects := make([]tool.ProjectMount, 0, len(items))
	for i, item := range items {
		project, err := parseLaunchProject(fmt.Sprintf("projects[%d]", i), item, dir, home)
		if err != nil {
			return nil, err
		}
		projects = append(projects, project)
	}
	return projects, nil
}

func parseLaunchProject(label string, raw any, dir, home string) (tool.ProjectMount, error) {
	name := ""
	path := "."
	switch value := raw.(type) {
	case string:
		name = value
	case map[string]any:
		for key, item := range value {
			switch key {
			case "name":
				nameValue, ok := item.(string)
				if !ok {
					return tool.ProjectMount{}, fmt.Errorf("%s.name must be a string", label)
				}
				name = nameValue
			case "path":
				pathValue, ok := item.(string)
				if !ok {
					return tool.ProjectMount{}, fmt.Errorf("%s.path must be a string", label)
				}
				path = pathValue
			default:
				return tool.ProjectMount{}, fmt.Errorf("unsupported %s key %q", label, key)
			}
		}
	default:
		return tool.ProjectMount{}, fmt.Errorf("%s must be a string or object", label)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return tool.ProjectMount{}, fmt.Errorf("%s.name must not be empty", label)
	}
	source, err := resolveLaunchProjectPath(path, dir, home)
	if err != nil {
		return tool.ProjectMount{}, fmt.Errorf("%s.path: %w", label, err)
	}
	return tool.ProjectMount{Name: name, Source: source}, nil
}

func parseLaunchTools(raw any) ([]launchToolConfig, error) {
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("tools must be an array")
	}
	tools := make([]launchToolConfig, 0, len(items))
	for i, item := range items {
		parsed, err := parseLaunchTool(fmt.Sprintf("tools[%d]", i), item, i == 0)
		if err != nil {
			return nil, err
		}
		tools = append(tools, parsed)
	}
	return tools, nil
}

func parseLaunchTool(label string, raw any, primary bool) (launchToolConfig, error) {
	name := ""
	var params []string
	switch value := raw.(type) {
	case string:
		name = value
	case map[string]any:
		for key, item := range value {
			switch key {
			case "name":
				nameValue, ok := item.(string)
				if !ok {
					return launchToolConfig{}, fmt.Errorf("%s.name must be a string", label)
				}
				name = nameValue
			case "params":
				if !primary {
					return launchToolConfig{}, fmt.Errorf("%s.params is only supported on the primary tool", label)
				}
				parsed, err := parseLaunchToolParams(label+".params", item)
				if err != nil {
					return launchToolConfig{}, err
				}
				params = parsed
			default:
				return launchToolConfig{}, fmt.Errorf("unsupported %s key %q", label, key)
			}
		}
	default:
		return launchToolConfig{}, fmt.Errorf("%s must be a string or object", label)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return launchToolConfig{}, fmt.Errorf("%s.name must not be empty", label)
	}
	return launchToolConfig{Name: name, Params: params}, nil
}

func parseLaunchToolParams(label string, raw any) ([]string, error) {
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an array", label)
	}
	params := make([]string, 0, len(items))
	for i, item := range items {
		value, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("%s[%d] must be a string", label, i)
		}
		params = append(params, value)
	}
	return params, nil
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

func joinConfigRelativePath(dir, path string) string {
	separator := string(filepath.Separator)
	if strings.HasSuffix(dir, separator) {
		return dir + path
	}
	return dir + separator + path
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
