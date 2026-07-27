package cli

// Classifies invocations that can execute without loading host configuration.

import (
	"strconv"
	"strings"
)

var invocationValueFlags = map[string]struct{}{
	"--config":  {},
	"--image":   {},
	"--project": {},
	"--pull":    {},
}

// IsConfigFreeInvocation reports whether arguments resolve to static CLI
// information or an agent command that does not consume launch configuration.
func IsConfigFreeInvocation(arguments []string) bool {
	if isConfigFreeAgentInvocation(arguments) {
		return true
	}
	if isConfigFreeVolumeInvocation(arguments) {
		return true
	}
	if isConfigFreeImageInvocation(arguments) {
		return true
	}
	if hasConflictingOutputModes(arguments) {
		return true
	}

	return isInformationalInvocation(arguments)
}

func hasConflictingOutputModes(arguments []string) bool {
	var debug, quiet bool
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if argument == "--" {
			break
		}
		if _, consumesValue := invocationValueFlags[argument]; consumesValue {
			index++
			continue
		}

		name, enabled, found := outputModeFlag(argument)
		if !found {
			continue
		}
		switch name {
		case "debug":
			debug = enabled
		case "quiet":
			quiet = enabled
		}
	}

	return debug && quiet
}

func outputModeFlag(argument string) (string, bool, bool) {
	switch argument {
	case "--debug":
		return "debug", true, true
	case "--quiet":
		return "quiet", true, true
	}

	for _, name := range [...]string{"debug", "quiet"} {
		value, found := strings.CutPrefix(argument, "--"+name+"=")
		if !found {
			continue
		}

		enabled, err := strconv.ParseBool(value)
		return name, enabled, err == nil
	}

	return "", false, false
}

func isInformationalInvocation(arguments []string) bool {
	if len(arguments) == 0 {
		return true
	}

	firstPositional := ""
	configuredLaunch := false
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if argument == "--" {
			break
		}
		if informationalFlagEnabled(argument) {
			return true
		}
		if _, consumesValue := invocationValueFlags[argument]; consumesValue {
			if argument == "--config" {
				configuredLaunch = true
			}
			index++
			continue
		}
		if strings.HasPrefix(argument, "--config=") {
			configuredLaunch = true
			continue
		}
		if strings.HasPrefix(argument, "-") {
			continue
		}
		if firstPositional == "" {
			firstPositional = argument
			if argument == "help" {
				return true
			}
		}
	}

	return firstPositional == "" && !configuredLaunch
}

func informationalFlagEnabled(argument string) bool {
	switch argument {
	case "--help", "-h", "--version":
		return true
	}

	for _, prefix := range [...]string{
		"--help=",
		"-h=",
		"--version=",
	} {
		value, found := strings.CutPrefix(argument, prefix)
		if !found {
			continue
		}

		enabled, err := strconv.ParseBool(value)
		return err == nil && enabled
	}

	return false
}
