package contextinit

import (
	"context"
	"sort"

	"petris.dev/toby/internal/config/toby"
	"petris.dev/toby/internal/context/files"
	"petris.dev/toby/internal/tools/tool"

	"go.uber.org/fx"
)

const FxGroup = "toby.context.init"

type Service interface {
	InitContext(context.Context, *tool.RunContext) error
}

type ServiceFunc func(context.Context, *tool.RunContext) error

func (f ServiceFunc) InitContext(ctx context.Context, run *tool.RunContext) error {
	return f(ctx, run)
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

func NewServices(cfg *tobyconfig.Service) Result {
	return Result{
		AgentInstructions: Registration{Name: "agent-instructions", Order: 10, Service: ServiceFunc(func(_ context.Context, run *tool.RunContext) error {
			return contextfiles.RegisterAgentInstructions(run.ContextFiles)
		})},
		TobyConfig: Registration{Name: "toby-config", Order: 20, Service: ServiceFunc(func(_ context.Context, run *tool.RunContext) error {
			if cfg == nil {
				return nil
			}
			return cfg.RegisterContextFiles(run.ContextFiles)
		})},
		Tools: Registration{Name: "tools", Order: 30, Service: ServiceFunc(func(ctx context.Context, run *tool.RunContext) error {
			return run.Toolset.RegisterContextFiles(ctx, run)
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
