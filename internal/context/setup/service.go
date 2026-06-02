package contextinit

import (
	"context"
	"io"
	"sort"

	"petris.dev/toby/internal/config/toby"
	"petris.dev/toby/internal/context/files"
	"petris.dev/toby/internal/tools/tool"

	"go.uber.org/fx"
)

const FxGroup = "toby.context.init"

type Service interface {
	InitContext(context.Context, Params) error
}

type Params struct {
	Toolset *tool.Toolset
	Options *tool.CommandOptions
	Stderr  io.Writer
}

type ServiceFunc func(context.Context, Params) error

func (f ServiceFunc) InitContext(ctx context.Context, params Params) error {
	return f(ctx, params)
}

type Registration struct {
	Name    string
	Order   int
	Service Service
}

type Result struct {
	fx.Out

	AgentInstructions Registration `group:"toby.context.init"`
	TobyConfig        Registration `group:"toby.context.init"`
	Tools             Registration `group:"toby.context.init"`
}

type HooksResult struct {
	fx.Out

	AgentInstructions tool.LifecycleHook `group:"toby.lifecycle.sandbox.context.init"`
	TobyConfig        tool.LifecycleHook `group:"toby.lifecycle.sandbox.context.init"`
}

func NewLifecycleHooks(cfg *tobyconfig.Service, contextFiles *contextfiles.Service) HooksResult {
	return HooksResult{
		AgentInstructions: tool.LifecycleHook{Name: "context.agent-instructions", Priority: -200, Run: func(ctx context.Context, _ tool.LifecycleContext) error {
			_, err := contextFiles.AddInstructionFS(ctx, contextfiles.GitAgentsPath, contextfiles.AgentFiles(), contextfiles.GitAgentsPath, 0o644)
			return err
		}},
		TobyConfig: tool.LifecycleHook{Name: "context.toby-config", Priority: -100, Run: func(ctx context.Context, _ tool.LifecycleContext) error {
			if cfg == nil {
				return nil
			}
			return cfg.RegisterContextFiles(ctx, contextFiles)
		}},
	}
}

func NewServices(cfg *tobyconfig.Service, contextFiles *contextfiles.Service) Result {
	return Result{
		AgentInstructions: Registration{Name: "agent-instructions", Order: 10, Service: ServiceFunc(func(ctx context.Context, _ Params) error {
			_, err := contextFiles.AddInstructionFS(ctx, contextfiles.GitAgentsPath, contextfiles.AgentFiles(), contextfiles.GitAgentsPath, 0o644)
			return err
		})},
		TobyConfig: Registration{Name: "toby-config", Order: 20, Service: ServiceFunc(func(ctx context.Context, _ Params) error {
			if cfg == nil {
				return nil
			}
			return cfg.RegisterContextFiles(ctx, contextFiles)
		})},
		Tools: Registration{Name: "tools", Order: 30, Service: ServiceFunc(func(ctx context.Context, params Params) error {
			var opts tool.ContextOptions
			if params.Options != nil {
				opts.SuppressWarnings = params.Options.SuppressWarnings
			}
			opts.Stderr = params.Stderr
			return params.Toolset.RegisterContextFiles(ctx, opts)
		})},
	}
}

func Ordered(registrations []Registration) []Service {
	items := append([]Registration(nil), registrations...)
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Order == items[j].Order {
			return items[i].Name < items[j].Name
		}
		return items[i].Order < items[j].Order
	})
	services := make([]Service, 0, len(items))
	for _, item := range items {
		if item.Service != nil {
			services = append(services, item.Service)
		}
	}
	return services
}
