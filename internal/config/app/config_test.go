package appconfig

// Exercises native host configuration merging, secret resolution, launch
// overrides, and host instruction loading.

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"petris.dev/toby/internal/agent/resource"
	"petris.dev/toby/internal/config"
	mcpconfig "petris.dev/toby/internal/config/mcp"
	modelsconfig "petris.dev/toby/internal/config/models"
	"petris.dev/toby/internal/diagnostic/warning"
	"petris.dev/toby/internal/oci/image"
	"petris.dev/toby/internal/oci/imagesource"
)

func TestLoadDeepMergesNativeConfiguration(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	writeConfig(t, filepath.Join(dir, "config.json"), mergedBaseJSONFixture)
	writeConfig(t, filepath.Join(dir, "config.yaml"), mergedOverrideYAMLFixture)
	writeConfig(t, filepath.Join(dir, "first.md"), "first")
	writeConfig(t, filepath.Join(dir, "second.md"), "second")

	service, err := Load(dir, home)
	if err != nil {
		t.Fatal(err)
	}
	instructions, err := service.ResolveInstructions(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(instructions) != 2 ||
		instructions[0].Source != filepath.Join(dir, "first.md") ||
		instructions[1].Source != filepath.Join(dir, "second.md") {
		t.Fatalf("instructions = %#v", instructions)
	}
	if got := service.Sandbox(); got.Image != "ghcr.io/example/final:latest" || got.Pull != image.PullAlways {
		t.Fatalf("sandbox = %#v", got)
	}
	settings := service.Settings()
	if settings.Debug == nil || !*settings.Debug {
		t.Fatalf("debug = %#v", settings.Debug)
	}
	if !settings.AllowExternalProjectsEnabled() {
		t.Fatal("allowExternalProjects was not enabled")
	}
	if !settings.SuppressWarnings.Suppresses(warning.PermissionAutoDeny) {
		t.Fatalf("suppression = %#v", settings.SuppressWarnings)
	}
	if got := service.PermissionPaths()[filepath.Join(home, "src")]; got != "allow" {
		t.Fatalf("permission = %q", got)
	}
	resources, err := service.ResolveResources(nil)
	if err != nil {
		t.Fatal(err)
	}
	if model, ok := resources.Models["openai"]; !ok ||
		model.Protocol != modelsconfig.ProtocolOpenAI {
		t.Fatalf("models resource = %#v, present %v", model, ok)
	}
}

func TestLoadDefaultsSandboxPullAndResolvesNativeMCPSecrets(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	t.Setenv("MCP_TOKEN", "environment-secret")
	t.Setenv("MCP_URL", "https://example.invalid/resolved-mcp")
	writeConfig(t, filepath.Join(dir, "argument.txt"), "file-secret\n")
	writeConfig(t, filepath.Join(dir, "config.yaml"), mcpResourcesYAMLFixture)

	service, err := Load(dir, home)
	if err != nil {
		t.Fatal(err)
	}
	if got := service.Sandbox(); got.Image != defaultSandboxImage ||
		got.Pull != image.PullIfMissing {
		t.Fatalf("default sandbox config = %#v", got)
	}

	resources, err := service.ResolveResources(nil)
	if err != nil {
		t.Fatal(err)
	}
	docs := resources.MCPs["docs"]
	if docs.URL != "https://example.invalid/resolved-mcp" ||
		docs.Headers["Authorization"] != "Bearer file-secret" {
		t.Fatalf("remote MCP = %#v", docs)
	}
	local := resources.MCPs["local"]
	if local.Type != mcpconfig.ServerLocal ||
		local.Scope != resource.ScopeRun ||
		local.Command[1] != "--token=file-secret" ||
		local.Environment["TOKEN"] != "environment-secret" {
		t.Fatalf("local MCP = %#v", local)
	}

	local.Environment["TOKEN"] = "changed"
	again, err := service.ResolveResources(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := again.MCPs["local"].Environment["TOKEN"]; got != "environment-secret" {
		t.Fatalf("MCP returned shared state: %q", got)
	}
}

func TestLoadResolvesSandboxBuildRelativeToConfig(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	writeConfig(
		t,
		filepath.Join(dir, "config.yaml"),
		"sandbox:\n"+
			"  build:\n"+
			"    context: ../source\n"+
			"    dockerfile: support/Tobyfile\n",
	)

	service, err := Load(dir, home)
	if err != nil {
		t.Fatal(err)
	}
	got := service.Sandbox()
	contextPath := filepath.Clean(filepath.Join(dir, "../source"))
	if got.Source != imagesource.Build ||
		got.Build.Context != contextPath ||
		got.Build.Dockerfile != filepath.Join(
			contextPath,
			"support",
			"Tobyfile",
		) ||
		got.Pull != image.PullIfMissing {
		t.Fatalf("sandbox = %#v", got)
	}
}

func TestLoadResolvesSandboxArchiveRelativeToConfig(t *testing.T) {
	dir := t.TempDir()
	writeConfig(
		t,
		filepath.Join(dir, "config.yaml"),
		"sandbox:\n"+
			"  archive: images/root.oci.tar\n",
	)

	service, err := Load(dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	got := service.Sandbox()
	if got.Source != imagesource.Archive ||
		got.Archive != filepath.Join(
			dir,
			"images",
			"root.oci.tar",
		) ||
		got.Pull != image.PullIfMissing {
		t.Fatalf("sandbox = %#v", got)
	}
}

func TestLoadAppliesPullPolicyToSandboxBuild(t *testing.T) {
	dir := t.TempDir()
	writeConfig(
		t,
		filepath.Join(dir, "config.yaml"),
		"sandbox:\n"+
			"  build:\n"+
			"    context: .\n"+
			"  pull: always\n",
	)

	service, err := Load(dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := service.Sandbox().Pull; got != image.PullAlways {
		t.Fatalf("pull policy = %q", got)
	}
}

func TestLoadRejectsMultipleSandboxSources(t *testing.T) {
	dir := t.TempDir()
	writeConfig(
		t,
		filepath.Join(dir, "config.yaml"),
		"sandbox:\n"+
			"  image: alpine\n"+
			"  archive: image.tar\n",
	)

	if _, err := Load(dir, t.TempDir()); err == nil {
		t.Fatal("multiple sandbox sources succeeded")
	}
}

func TestLoadSkipsMissingMCPEnvironmentSubstitution(t *testing.T) {
	const environmentName = "TOBY_TEST_MISSING_MCP_URL"

	previous, present := os.LookupEnv(environmentName)
	if err := os.Unsetenv(environmentName); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if present {
			if err := os.Setenv(environmentName, previous); err != nil {
				t.Errorf("restore %s: %v", environmentName, err)
			}
			return
		}
		if err := os.Unsetenv(environmentName); err != nil {
			t.Errorf("unset %s: %v", environmentName, err)
		}
	})

	dir := t.TempDir()
	writeConfig(t, filepath.Join(dir, "config.yaml"), missingMCPEnvironmentYAMLFixture)

	service, err := Load(dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	resources, err := service.ResolveResources(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := resources.MCPs["docs"]; ok {
		t.Fatalf("skipped MCP remained: %#v", resources.MCPs)
	}
}

func TestNewDefaultsInitializesEmptyValidConfiguration(t *testing.T) {
	home := t.TempDir()
	paths := config.Paths{
		Home:          home,
		XDGConfigHome: filepath.Join(home, ".config"),
	}

	service := NewDefaults(paths)
	if service.Dir != paths.TobyConfigDir() || service.Home != home {
		t.Fatalf(
			"default service paths = dir %q home %q",
			service.Dir,
			service.Home,
		)
	}
	if service.config.Permissions.Paths == nil ||
		service.config.Permissions.Actions == nil {
		t.Fatalf("default config contains nil maps: %#v", service.config)
	}
	if got, err := service.ResolveResources(nil); err != nil ||
		len(got.MCPs) != 0 ||
		len(got.Models) != 0 {
		t.Fatalf("default resources = %#v, error %v", got, err)
	}
	if got := service.Sandbox(); got.Image != defaultSandboxImage ||
		got.Pull != image.PullIfMissing {
		t.Fatalf("default sandbox config = %#v", got)
	}
	if err := service.config.Validate(); err != nil {
		t.Fatalf("default config validation: %v", err)
	}
}

func TestPermissionPathsDefaultToTemporaryDirectory(t *testing.T) {
	service := NewDefaults(config.Paths{})

	if got, want := service.PermissionPaths(), map[string]string{
		"/tmp": "allow",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("permission paths = %#v, want %#v", got, want)
	}

	service.config.Permissions.Paths["/tmp"] = "deny"
	if got := service.PermissionPaths()["/tmp"]; got != "deny" {
		t.Fatalf("configured /tmp permission = %q, want deny", got)
	}

	yolo := true
	service.config.Settings.Yolo = &yolo
	if got := service.PermissionPaths()["/"]; got != "allow" {
		t.Fatalf("yolo root permission = %q, want allow", got)
	}
}

func TestLoadRejectsInvalidNativeConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		config  string
		wantErr string
	}{
		{
			name:    "pull policy",
			config:  "sandbox:\n  pull: sometimes\n",
			wantErr: "sandbox.pull has unsupported value",
		},
		{
			name:    "unknown top level",
			config:  "unexpected: true\n",
			wantErr: `unknown field "unexpected"`,
		},
		{
			name:    "null resources block",
			config:  "resources: null\n",
			wantErr: "resources must be an object",
		},
		{
			name:    "null MCP resources block",
			config:  "resources:\n  mcps: null\n",
			wantErr: "resources.mcps must be an object",
		},
		{
			name:    "null models resources block",
			config:  "resources:\n  models: null\n",
			wantErr: "resources.models must be an object",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			writeConfig(t, filepath.Join(dir, "config.yaml"), test.config)

			service, err := Load(dir, t.TempDir())
			if err == nil && strings.Contains(
				test.config,
				"resources:",
			) {
				_, err = service.ResolveResources(nil)
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestWithOverridesAndLaunchHolder(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, filepath.Join(dir, "config.yaml"), effectiveSettingsYAMLFixture)
	base, err := Load(dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	debug := true
	effective := base.WithOverrides(LaunchOverrides{
		Image: "ghcr.io/example/launch:latest",
		Pull:  image.PullNever,
		Debug: &debug,
	})
	if got := effective.Sandbox(); got.Image != "ghcr.io/example/launch:latest" || got.Pull != image.PullNever {
		t.Fatalf("effective sandbox = %#v", got)
	}
	if got := base.Sandbox(); got.Image != "ghcr.io/example/base:latest" || got.Pull != image.PullIfMissing {
		t.Fatalf("base sandbox mutated = %#v", got)
	}

	holder := NewLaunchHolder(base)
	if holder.Current() != base {
		t.Fatal("holder did not start with base config")
	}
	holder.SetCurrent(effective)
	if holder.Current() != effective {
		t.Fatal("holder did not expose effective config")
	}
}

func TestLoadAndOverrideToolProfiles(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "toby")
	writeConfig(t, filepath.Join(dir, "config.yaml"), toolProfilesYAMLFixture)

	cfg, err := Load(dir, home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Profile() != "work" {
		t.Fatalf("profile = %q, want work", cfg.Profile())
	}
	if got, want := cfg.ToolProfiles(), map[string]string{
		"opencode": "personal",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tool profiles = %#v, want %#v", got, want)
	}

	effective := cfg.WithOverrides(LaunchOverrides{
		Profile:      "review",
		ToolProfiles: map[string]string{"codex": "shared"},
	})
	if effective.Profile() != "review" {
		t.Fatalf("effective profile = %q, want review", effective.Profile())
	}
	if got, want := effective.ToolProfiles(), map[string]string{
		"opencode": "personal",
		"codex":    "shared",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf(
			"effective tool profiles = %#v, want %#v",
			got,
			want,
		)
	}
	if cfg.Profile() != "work" ||
		!reflect.DeepEqual(
			cfg.ToolProfiles(),
			map[string]string{"opencode": "personal"},
		) {
		t.Fatal("WithOverrides mutated the base profile configuration")
	}
}

func TestDefaultProfileIsExplicit(t *testing.T) {
	cfg, err := Load(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Profile() != "default" || cfg.Settings().Profile != "default" {
		t.Fatalf("default profile = %#v", cfg.Settings().Profile)
	}
}

func TestResolveInstructionsReturnsHostSourcesAndDetachedContents(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	first := filepath.Join(dir, "instructions", "first.md")
	second := filepath.Join(dir, "instructions", "second.md")
	writeConfig(t, first, "first\n")
	writeConfig(t, second, "second\n")
	writeConfig(t, filepath.Join(dir, "config.yaml"), instructionGlobYAMLFixture)

	service, err := Load(dir, home)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := service.ResolveInstructions(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 2 ||
		resolved[0].Source != first ||
		string(resolved[0].Contents) != "first\n" ||
		resolved[1].Source != second ||
		string(resolved[1].Contents) != "second\n" {
		t.Fatalf("resolved instructions = %#v", resolved)
	}
	resolved[0].Contents[0] = 'X'
	again, err := service.ResolveInstructions(nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(again[0].Contents) != "first\n" {
		t.Fatal("resolved instruction contents shared mutable state")
	}
}

func TestLoadSkipsInvalidPermissionPathMode(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	writeConfig(
		t,
		filepath.Join(dir, "config.yaml"),
		"permissions:\n  paths:\n    ~/src: allow\n    ~/bad: alow\n",
	)

	service, err := Load(dir, home)
	if err != nil {
		t.Fatal(err)
	}
	if got := service.PermissionPaths()[filepath.Join(home, "src")]; got != "allow" {
		t.Fatalf("valid path mode = %q", got)
	}
	if _, ok := service.PermissionPaths()[filepath.Join(home, "bad")]; ok {
		t.Fatal("invalid path mode was kept")
	}

	logger := &configWarningLogger{}
	service.EmitPendingWarnings(warning.NewService(logger, nil))
	if len(logger.messages) != 1 ||
		!strings.Contains(logger.messages[0], string(warning.PermissionPathInvalid)) {
		t.Fatalf("warnings = %#v", logger.messages)
	}
}

func TestResolveInstructionsSkipsMissingSources(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, filepath.Join(dir, "present.md"), "present\n")
	writeConfig(
		t,
		filepath.Join(dir, "config.yaml"),
		"instructions:\n  - present.md\n  - missing.md\n  - absent/*.md\n",
	)

	service, err := Load(dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	logger := &configWarningLogger{}
	resolved, err := service.ResolveInstructions(warning.NewService(logger, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 1 ||
		resolved[0].Source != filepath.Join(dir, "present.md") ||
		string(resolved[0].Contents) != "present\n" {
		t.Fatalf("resolved = %#v", resolved)
	}
	if len(logger.messages) != 2 {
		t.Fatalf("warnings = %#v", logger.messages)
	}
	for _, message := range logger.messages {
		if !strings.Contains(message, string(warning.ConfigInstructionMissing)) {
			t.Fatalf("warning = %q", message)
		}
	}
}

func TestResolveResourcesSkipsInvalidOptionalEntries(t *testing.T) {
	dir := t.TempDir()
	writeConfig(
		t,
		filepath.Join(dir, "config.yaml"),
		"resources:\n"+
			"  mcps:\n"+
			"    good:\n"+
			"      type: remote\n"+
			"      transport: http\n"+
			"      url: https://example.invalid/mcp\n"+
			"    bad: not-an-object\n"+
			"  models:\n"+
			"    good:\n"+
			"      protocol: openai\n"+
			"      url: https://example.invalid/v1\n"+
			"    bad:\n"+
			"      protocol: openai\n",
	)

	service, err := Load(dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	logger := &configWarningLogger{}
	resources, err := service.ResolveResources(warning.NewService(logger, nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := resources.MCPs["good"]; !ok {
		t.Fatalf("valid MCP was skipped: %#v", resources.MCPs)
	}
	if _, ok := resources.MCPs["bad"]; ok {
		t.Fatalf("invalid MCP was kept: %#v", resources.MCPs)
	}
	if _, ok := resources.Models["good"]; !ok {
		t.Fatalf("valid models endpoint was skipped: %#v", resources.Models)
	}
	if _, ok := resources.Models["bad"]; ok {
		t.Fatalf("invalid models endpoint was kept: %#v", resources.Models)
	}
	joined := strings.Join(logger.messages, "\n")
	if !strings.Contains(joined, string(warning.MCPServerInvalid)) ||
		!strings.Contains(joined, string(warning.ModelsEndpointUnavailable)) {
		t.Fatalf("warnings = %q", joined)
	}
}

type configWarningLogger struct {
	messages []string
}

func (l *configWarningLogger) Warn(message string, _ ...any) {
	l.messages = append(l.messages, message)
}

func (l *configWarningLogger) WarnError(
	message string,
	_ error,
	_ ...any,
) {
	l.messages = append(l.messages, message)
}

func (l *configWarningLogger) Error(message string, _ ...any) {
	l.messages = append(l.messages, message)
}

func TestResolveStringSubstitutions(t *testing.T) {
	firstDir := t.TempDir()
	secondDir := t.TempDir()
	home := t.TempDir()
	t.Setenv("API_TOKEN", "token")
	writeConfig(t, filepath.Join(secondDir, "secret.txt"), "relative\n")
	writeConfig(t, filepath.Join(home, "home-secret.txt"), "home\n")

	resolved, err := resolveString(
		"env={env:API_TOKEN}; rel={file:secret.txt}; home={file:~/home-secret.txt}",
		[]string{firstDir, secondDir},
		home,
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != "env=token; rel=relative; home=home" {
		t.Fatalf("resolved = %q", resolved)
	}
}

func writeConfig(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
