package cli

import (
	"strings"

	"petris.dev/toby/internal/exitcode"
	"petris.dev/toby/internal/tool"
)

type parsedCommand struct {
	Options        tool.CommandOptions
	Extra          []string
	RequestedTools []string
	Help           bool
}

func parseSandboxArgs(raw []string, launch bool, primary string, contextTools []tool.Tool, toolArgParser func(string, string) (bool, string, error)) (parsedCommand, error) {
	var result parsedCommand
	scriptArgs, passthrough := splitPassthrough(raw)
	withFlags := map[string]string{}
	for _, item := range contextTools {
		if item.Name() == primary {
			continue
		}
		withFlags["--with-"+item.CommandName()] = item.Name()
	}

	seenEnv := false
	for i := 0; i < len(scriptArgs); i++ {
		arg := scriptArgs[i]
		if arg == "--help" || arg == "-h" {
			result.Help = true
			return result, nil
		}
		if launch && arg == "--install" {
			result.Options.Install = true
			continue
		}
		if launch && arg == "--upgrade" {
			result.Options.Upgrade = true
			continue
		}
		if value, ok := strings.CutPrefix(arg, "--project="); ok {
			result.Options.Project = value
			continue
		}
		if arg == "--project" {
			if i+1 >= len(scriptArgs) {
				return result, exitcode.New(2, "--project requires a value")
			}
			i++
			result.Options.Project = scriptArgs[i]
			continue
		}
		if value, ok := strings.CutPrefix(arg, "--sandbox-runtime="); ok {
			result.Options.SandboxRuntime = value
			continue
		}
		if arg == "--sandbox-runtime" {
			if i+1 >= len(scriptArgs) {
				return result, exitcode.New(2, "--sandbox-runtime requires a value")
			}
			i++
			result.Options.SandboxRuntime = scriptArgs[i]
			continue
		}
		if value, ok := strings.CutPrefix(arg, "--sandbox-image="); ok {
			result.Options.DockerImage = value
			continue
		}
		if arg == "--sandbox-image" {
			if i+1 >= len(scriptArgs) {
				return result, exitcode.New(2, "--sandbox-image requires a value")
			}
			i++
			result.Options.DockerImage = scriptArgs[i]
			continue
		}
		if toolName, ok := withFlags[arg]; ok {
			result.RequestedTools = appendIfMissing(result.RequestedTools, toolName)
			continue
		}
		if toolArgParser != nil {
			handled, replacement, err := toolArgParser(arg, nextArg(scriptArgs, i))
			if err != nil {
				return result, err
			}
			if handled {
				if replacement != "" {
					i++
				}
				continue
			}
		}
		if strings.HasPrefix(arg, "-") {
			result.Extra = append(result.Extra, scriptArgs[i:]...)
			break
		}
		if !seenEnv {
			result.Options.Env = arg
			seenEnv = true
			continue
		}
		result.Extra = append(result.Extra, scriptArgs[i:]...)
		break
	}
	if result.Options.Install && result.Options.Upgrade {
		return result, exitcode.New(2, "--install and --upgrade are mutually exclusive")
	}
	if primary != "" {
		result.RequestedTools = appendIfMissing(result.RequestedTools, primary)
	}
	result.Extra = append(result.Extra, passthrough...)
	return result, nil
}

func splitPassthrough(raw []string) ([]string, []string) {
	for i, arg := range raw {
		if arg == "--" {
			return raw[:i], raw[i+1:]
		}
	}
	return raw, nil
}

func nextArg(args []string, i int) string {
	if i+1 >= len(args) {
		return ""
	}
	return args[i+1]
}

func appendIfMissing(values []string, value string) []string {
	for _, item := range values {
		if item == value {
			return values
		}
	}
	return append(values, value)
}
