package tools

import (
	"net/http"
	"time"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/tool"

	"go.uber.org/fx"
)

type constructor any

var constructors []constructor

func register(c constructor) {
	constructors = append(constructors, c)
}

func Module() fx.Option {
	options := []fx.Option{
		fx.Provide(newHTTPClient),
	}
	for _, c := range constructors {
		options = append(options, fx.Provide(fx.Annotate(c, fx.ResultTags(`group:"`+tool.FxToolGroup+`"`))))
	}
	return fx.Module("tools", options...)
}

func newHTTPClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}

func simpleBase(name, help string, groups ...string) tool.Base {
	return tool.Base{Metadata: tool.Metadata{Name: name, LaunchHelp: help, ContextGroups: groups}}
}

func simpleBaseWithDeps(name, help string, deps []string, groups ...string) tool.Base {
	return tool.Base{Metadata: tool.Metadata{Name: name, LaunchHelp: help, Dependencies: deps, ContextGroups: groups}}
}

func simpleTool(paths config.Paths, base tool.Base, hostSubpath, sandboxSubpath []string, install []string, sandboxEnv map[string]string) tool.Tool {
	return &tool.Simple{
		Base:           base,
		RootDir:        paths.SandboxRoot,
		Home:           paths.Home,
		HostSubpath:    hostSubpath,
		SandboxSubpath: sandboxSubpath,
		InstallCommand: install,
		SandboxEnv:     sandboxEnv,
	}
}
