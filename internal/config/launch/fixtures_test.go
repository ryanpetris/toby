package launchconfig

// Embeds package-local static payloads used by tests.

import _ "embed"

//go:embed testdata/config-set-base.yaml
var configSetBaseYAMLFixture string

//go:embed testdata/config-set-machine.yaml
var configSetMachineYAMLFixture string

//go:embed testdata/config-set-team.yaml
var configSetTeamYAMLFixture string

//go:embed testdata/yaml-implicit-types.yaml
var yamlImplicitTypesFixture string

//go:embed testdata/yaml-anchor-base.yaml
var yamlAnchorBaseFixture string

//go:embed testdata/yaml-anchor-fragment.yaml
var yamlAnchorFragmentFixture string

//go:embed testdata/fragment-escape-base.yaml
var fragmentEscapeBaseFixture string

//go:embed testdata/fragment-escape-override.yaml
var fragmentEscapeOverrideFixture string

//go:embed testdata/yaml-alias-expansion.yaml
var yamlAliasExpansionFixture string

//go:embed testdata/full-launch-prefix.yaml
var fullLaunchPrefixFixture string

//go:embed testdata/full-launch-suffix.yaml
var fullLaunchSuffixFixture string

//go:embed testdata/nullable-launch.yaml
var nullableLaunchFixture string

//go:embed testdata/exec-primary.yaml
var execPrimaryFixture string

//go:embed testdata/custom-projects.yaml
var customProjectsFixture string

//go:embed testdata/project-name.yaml
var projectNameFixture string

//go:embed testdata/secondary-tool-params.yaml
var secondaryToolParamsFixture string

//go:embed testdata/unknown-warning.yaml
var unknownWarningFixture string

//go:embed testdata/unknown-tool.yaml
var unknownToolFixture string

//go:embed testdata/project-autoload.yaml
var projectAutoloadFixture string

//go:embed testdata/autoload-setting.yaml
var autoloadSettingFixture string

//go:embed testdata/sandbox-image.yaml
var sandboxImageFixture string
