package launchconfig

// Tests for canonical project configuration sets, including deterministic
// fragment merging, root-relative resolution, and safe file-type validation.

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/diagnostic/warning"
	"petris.dev/toby/internal/oci/image"
	"petris.dev/toby/internal/tools"

	"gopkg.in/yaml.v3"
)

func TestLoadProjectConfigSetMergesFragmentsInLexicalOrder(t *testing.T) {
	home := t.TempDir()
	projectRoot := filepath.Join(home, "project")
	configPath := filepath.Join(projectRoot, projectLaunchConfigName)
	writeTestFile(t, configPath, []byte(configSetBaseYAMLFixture))

	fragments := filepath.Join(projectRoot, projectConfigDirName, projectFragmentsDir)
	writeTestFile(t, filepath.Join(fragments, "90-machine.yaml"), []byte(configSetMachineYAMLFixture))
	writeTestFile(t, filepath.Join(fragments, "10-team.yaml"), []byte(configSetTeamYAMLFixture))
	writeTestFile(t, filepath.Join(fragments, "50-empty.yaml"), nil)
	writeTestFile(t, filepath.Join(fragments, "95-ignored.yml"), []byte("invalid: ["))
	writeTestFile(t, filepath.Join(fragments, "README"), []byte("not configuration"))

	cfg, err := loadLaunchConfigWithPaths(configPath, config.Paths{Home: home, ProjectRoot: filepath.Join(home, "Projects")}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Name != "final" || cfg.Sandbox.Image != "final-image" {
		t.Fatalf("name/image = %q/%q", cfg.Name, cfg.Sandbox.Image)
	}
	if cfg.Settings.Debug == nil || !*cfg.Settings.Debug {
		t.Fatalf("debug = %#v", cfg.Settings.Debug)
	}
	if got, want := cfg.Sandbox.Pull, image.PullNever; got != want {
		t.Fatalf("pull = %q, want %q", got, want)
	}
	if got, want := projectMounts(cfg.Projects), []tools.ProjectMount{
		{Name: "app", Source: filepath.Join(projectRoot, "src", "final")},
		{Name: "root", Source: projectRoot},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("projects = %#v, want %#v", got, want)
	}
	if got, want := cfg.Tools[0].Params, []string{"final", "--flag"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("params = %#v, want %#v", got, want)
	}
}

func TestLoadProjectConfigSetWarnsOnNonYAMLFragments(t *testing.T) {
	home := t.TempDir()
	projectRoot := filepath.Join(home, "project")
	configPath := filepath.Join(projectRoot, projectLaunchConfigName)
	writeTestFile(t, configPath, []byte("name: base\n"))
	fragments := filepath.Join(projectRoot, projectConfigDirName, projectFragmentsDir)
	writeTestFile(t, filepath.Join(fragments, "95-ignored.yml"), []byte("name: ignored\n"))
	writeTestFile(t, filepath.Join(fragments, "README"), []byte("not configuration"))

	logger := &configSetWarningLogger{}
	cfg, err := loadLaunchConfig(
		configPath,
		config.Paths{Home: home, ProjectRoot: filepath.Join(home, "Projects")},
		nil,
		warning.NewService(logger, nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "base" {
		t.Fatalf("name = %q", cfg.Name)
	}
	if len(logger.messages) != 2 {
		t.Fatalf("warnings = %#v", logger.messages)
	}
	for _, message := range logger.messages {
		if !strings.Contains(message, string(warning.ConfigFragmentIgnored)) {
			t.Fatalf("warning = %q", message)
		}
	}
}

type configSetWarningLogger struct {
	messages []string
}

func (l *configSetWarningLogger) Warn(message string, _ ...any) {
	l.messages = append(l.messages, message)
}

func (l *configSetWarningLogger) WarnError(
	message string,
	_ error,
	_ ...any,
) {
	l.messages = append(l.messages, message)
}

func (l *configSetWarningLogger) Error(message string, _ ...any) {
	l.messages = append(l.messages, message)
}

func TestLoadProjectConfigSetAllowsMissingOrEmptyFragmentDirectory(t *testing.T) {
	for _, name := range []string{"missing", "empty"} {
		t.Run(name, func(t *testing.T) {
			projectRoot := t.TempDir()
			configPath := filepath.Join(projectRoot, projectLaunchConfigName)
			writeTestFile(t, configPath, []byte("name: "+name+"\n"))
			if name == "empty" {
				if err := os.Mkdir(filepath.Join(projectRoot, projectConfigDirName, projectFragmentsDir), 0o700); err != nil {
					t.Fatal(err)
				}
			}

			cfg, err := loadLaunchConfigWithPaths(configPath, config.Paths{Home: t.TempDir()}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Name != name {
				t.Fatalf("name = %q, want %q", cfg.Name, name)
			}
		})
	}
}

func TestLoadProjectConfigSetPreservesYAMLScalarLexemes(t *testing.T) {
	projectRoot := t.TempDir()
	configPath := filepath.Join(projectRoot, projectLaunchConfigName)
	writeTestFile(t, configPath, []byte(yamlImplicitTypesFixture))
	writeTestFile(t, filepath.Join(projectRoot, projectConfigDirName, projectFragmentsDir, "20-settings.yaml"), []byte("settings:\n  debug: true\n"))

	cfg, err := loadLaunchConfigWithPaths(configPath, config.Paths{Home: t.TempDir()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "2026-01-01" {
		t.Fatalf("name = %q, want original YAML scalar", cfg.Name)
	}
	if got, want := cfg.Tools[0].Params, []string{"0x10"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("params = %#v, want %#v", got, want)
	}
}

func TestLoadProjectConfigSetAllowsNullToReplaceMapping(t *testing.T) {
	projectRoot := t.TempDir()
	configPath := filepath.Join(projectRoot, projectLaunchConfigName)
	writeTestFile(t, configPath, []byte("settings:\n  debug: true\n"))
	writeTestFile(t, filepath.Join(projectRoot, projectConfigDirName, projectFragmentsDir, "20-reset.yaml"), []byte("settings: null\n"))

	cfg, err := loadLaunchConfigWithPaths(configPath, config.Paths{Home: t.TempDir()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Settings.Debug != nil {
		t.Fatalf("debug = %#v, want nil after null reset", cfg.Settings.Debug)
	}
}

func TestLoadProjectConfigSetNormalizesAliasesAndMergeKeysPerSource(t *testing.T) {
	projectRoot := t.TempDir()
	configPath := filepath.Join(projectRoot, projectLaunchConfigName)
	writeTestFile(t, configPath, []byte(yamlAnchorBaseFixture))
	writeTestFile(t, filepath.Join(projectRoot, projectConfigDirName, projectFragmentsDir, "20-override.yaml"), []byte(yamlAnchorFragmentFixture))

	cfg, err := loadLaunchConfigWithPaths(configPath, config.Paths{Home: t.TempDir()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "new" || cfg.Workdir != "old" {
		t.Fatalf("name/workdir = %q/%q, want new/old", cfg.Name, cfg.Workdir)
	}
	if len(cfg.Tools) != 1 || !cfg.Tools[0].Primary {
		t.Fatalf("tools = %#v, want merged primary", cfg.Tools)
	}
	if got, want := cfg.Tools[0].Params, []string{"fragment"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("params = %#v, want %#v", got, want)
	}
}

func TestLoadArbitraryConfigDoesNotDiscoverFragments(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "configs")
	configPath := filepath.Join(dir, "config.yaml")
	writeTestFile(t, configPath, []byte(fragmentEscapeBaseFixture))
	writeTestFile(t, filepath.Join(dir, projectFragmentsDir, "90-local.yaml"), []byte(fragmentEscapeOverrideFixture))

	cfg, err := loadLaunchConfigWithPaths(configPath, config.Paths{Home: home}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "base" {
		t.Fatalf("name = %q, want base", cfg.Name)
	}
	if got, want := cfg.Sandbox.Image, "base-image"; got != want {
		t.Fatalf("sandbox image = %q, want %q", got, want)
	}
	if got, want := cfg.Projects[0].Mount.Source, filepath.Join(dir, "src"); got != want {
		t.Fatalf("project = %q, want %q", got, want)
	}
}

func TestLoadProjectConfigSetReportsSourceAndStrictDecodeErrors(t *testing.T) {
	t.Run("fragment syntax", func(t *testing.T) {
		projectRoot := t.TempDir()
		configPath := filepath.Join(projectRoot, projectLaunchConfigName)
		writeTestFile(t, configPath, []byte("name: base\n"))
		fragment := filepath.Join(projectRoot, projectConfigDirName, projectFragmentsDir, "20-broken.yaml")
		writeTestFile(t, fragment, []byte("settings: ["))

		_, err := loadLaunchConfigWithPaths(configPath, config.Paths{Home: t.TempDir()}, nil)
		if err == nil || !strings.Contains(err.Error(), fragment) {
			t.Fatalf("error = %v, want source path %q", err, fragment)
		}
	})

	t.Run("unknown merged field", func(t *testing.T) {
		projectRoot := t.TempDir()
		configPath := filepath.Join(projectRoot, projectLaunchConfigName)
		writeTestFile(t, configPath, []byte("name: base\n"))
		fragment := filepath.Join(projectRoot, projectConfigDirName, projectFragmentsDir, "20-unknown.yaml")
		writeTestFile(t, fragment, []byte("unknown: true\n"))

		_, err := loadLaunchConfigWithPaths(configPath, config.Paths{Home: t.TempDir()}, nil)
		if err == nil || !strings.Contains(err.Error(), fragment) || !strings.Contains(err.Error(), "field unknown not found") {
			t.Fatalf("error = %v, want source-named strict unknown-field error", err)
		}
	})

	t.Run("miscased merged field", func(t *testing.T) {
		projectRoot := t.TempDir()
		configPath := filepath.Join(projectRoot, projectLaunchConfigName)
		writeTestFile(t, configPath, []byte("name: base\n"))
		fragment := filepath.Join(projectRoot, projectConfigDirName, projectFragmentsDir, "20-miscased.yaml")
		writeTestFile(t, fragment, []byte("Name: changed\n"))

		_, err := loadLaunchConfigWithPaths(configPath, config.Paths{Home: t.TempDir()}, nil)
		if err == nil || !strings.Contains(err.Error(), fragment) || !strings.Contains(err.Error(), "field Name not found") {
			t.Fatalf("error = %v, want source-named strict miscased-field error", err)
		}
	})

	t.Run("overwritten invalid type", func(t *testing.T) {
		projectRoot := t.TempDir()
		configPath := filepath.Join(projectRoot, projectLaunchConfigName)
		writeTestFile(t, configPath, []byte("name: base\n"))
		fragments := filepath.Join(projectRoot, projectConfigDirName, projectFragmentsDir)
		invalid := filepath.Join(fragments, "10-invalid.yaml")
		writeTestFile(t, invalid, []byte("settings:\n  debug: wrong\n"))
		writeTestFile(t, filepath.Join(fragments, "20-valid.yaml"), []byte("settings:\n  debug: true\n"))

		_, err := loadLaunchConfigWithPaths(configPath, config.Paths{Home: t.TempDir()}, nil)
		if err == nil || !strings.Contains(err.Error(), invalid) || !strings.Contains(err.Error(), "settings.debug") {
			t.Fatalf("error = %v, want source-named type error", err)
		}
	})

	t.Run("duplicate source field", func(t *testing.T) {
		projectRoot := t.TempDir()
		configPath := filepath.Join(projectRoot, projectLaunchConfigName)
		writeTestFile(t, configPath, []byte("name: first\nname: second\n"))

		_, err := loadLaunchConfigWithPaths(configPath, config.Paths{Home: t.TempDir()}, nil)
		if err == nil || !strings.Contains(err.Error(), `duplicate config key "name"`) {
			t.Fatalf("error = %v, want duplicate-field error", err)
		}
	})
}

func TestLoadLaunchConfigRejectsMultipleYAMLDocuments(t *testing.T) {
	t.Run("canonical fragment", func(t *testing.T) {
		projectRoot := t.TempDir()
		configPath := filepath.Join(projectRoot, projectLaunchConfigName)
		writeTestFile(t, configPath, []byte("name: base\n"))
		fragment := filepath.Join(projectRoot, projectConfigDirName, projectFragmentsDir, "20-multiple.yaml")
		writeTestFile(t, fragment, []byte("settings:\n  debug: true\n---\nunknown: true\n"))

		_, err := loadLaunchConfigWithPaths(configPath, config.Paths{Home: t.TempDir()}, nil)
		if err == nil || !strings.Contains(err.Error(), fragment) || !strings.Contains(err.Error(), "multiple YAML documents") {
			t.Fatalf("error = %v, want source-named multiple-document error", err)
		}
	})

	t.Run("arbitrary config", func(t *testing.T) {
		configPath := filepath.Join(t.TempDir(), "launch.yaml")
		writeTestFile(t, configPath, []byte("name: base\n---\nname: ignored\n"))

		_, err := loadLaunchConfigWithPaths(configPath, config.Paths{Home: t.TempDir()}, nil)
		if err == nil || !strings.Contains(err.Error(), configPath) || !strings.Contains(err.Error(), "multiple YAML documents") {
			t.Fatalf("error = %v, want source-named multiple-document error", err)
		}
	})
}

func TestNormalizeLaunchYAMLBoundsAliasExpansion(t *testing.T) {
	data := []byte(yamlAliasExpansionFixture)
	var document yaml.Node
	if err := decodeSingleYAMLDocument(data, "bomb.yaml", &document); err != nil {
		t.Fatal(err)
	}
	mapping, empty, err := launchSourceMapping(&document, "bomb.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if empty {
		t.Fatal("alias expansion fixture unexpectedly decoded empty")
	}

	_, err = normalizeLaunchYAML(mapping, "bomb.yaml", len(data))
	if err == nil || !strings.Contains(err.Error(), "bomb.yaml") || !strings.Contains(err.Error(), "configuration size limit") {
		t.Fatalf("error = %v, want bounded alias-expansion error", err)
	}
}

func TestLoadProjectConfigSetRejectsUnsafeTypes(t *testing.T) {
	t.Run("config directory symlink", func(t *testing.T) {
		projectRoot := t.TempDir()
		actual := filepath.Join(projectRoot, "actual")
		writeTestFile(t, filepath.Join(actual, projectConfigFileName), []byte("name: linked\n"))
		if err := os.Symlink("actual", filepath.Join(projectRoot, projectConfigDirName)); err != nil {
			t.Fatal(err)
		}

		configPath := filepath.Join(projectRoot, projectLaunchConfigName)
		_, err := loadLaunchConfigWithPaths(configPath, config.Paths{Home: t.TempDir()}, nil)
		if err == nil || !strings.Contains(err.Error(), "must not be a symbolic link") {
			t.Fatalf("error = %v, want symbolic-link error", err)
		}
	})

	t.Run("base symlink", func(t *testing.T) {
		projectRoot := t.TempDir()
		target := filepath.Join(projectRoot, "base.yaml")
		writeTestFile(t, target, []byte("name: linked\n"))
		configDir := filepath.Join(projectRoot, projectConfigDirName)
		if err := os.MkdirAll(configDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(configDir, projectConfigFileName)); err != nil {
			t.Fatal(err)
		}

		configPath := filepath.Join(projectRoot, projectLaunchConfigName)
		_, err := loadLaunchConfigWithPaths(configPath, config.Paths{Home: t.TempDir()}, nil)
		if err == nil || !strings.Contains(err.Error(), "must not be a symbolic link") {
			t.Fatalf("error = %v, want symbolic-link error", err)
		}
	})

	t.Run("fragment symlink", func(t *testing.T) {
		projectRoot := t.TempDir()
		configPath := filepath.Join(projectRoot, projectLaunchConfigName)
		writeTestFile(t, configPath, []byte("name: base\n"))
		fragments := filepath.Join(projectRoot, projectConfigDirName, projectFragmentsDir)
		target := filepath.Join(fragments, "target.txt")
		writeTestFile(t, target, []byte("name: linked\n"))
		if err := os.Symlink("target.txt", filepath.Join(fragments, "20-linked.yaml")); err != nil {
			t.Fatal(err)
		}

		_, err := loadLaunchConfigWithPaths(configPath, config.Paths{Home: t.TempDir()}, nil)
		if err == nil || !strings.Contains(err.Error(), "must not be a symbolic link") {
			t.Fatalf("error = %v, want symbolic-link error", err)
		}
	})

	t.Run("fragment directory without base", func(t *testing.T) {
		projectRoot := t.TempDir()
		fragments := filepath.Join(projectRoot, projectConfigDirName, projectFragmentsDir)
		writeTestFile(t, filepath.Join(fragments, "20-only.yaml"), []byte("name: fragment\n"))

		configPath := filepath.Join(projectRoot, projectLaunchConfigName)
		_, err := loadLaunchConfigWithPaths(configPath, config.Paths{Home: t.TempDir()}, nil)
		if err == nil || !strings.Contains(err.Error(), projectConfigFileName) {
			t.Fatalf("error = %v, want missing base error", err)
		}
	})
}

func loadLaunchConfigWithPaths(
	path string,
	paths config.Paths,
	logger *diagnostic.Logger,
) (launchConfig, error) {
	return loadLaunchConfig(path, paths, logger, nil)
}
