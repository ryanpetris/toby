package agent

// Provides the agent server graph and the launch-side client graph.

import (
	"petris.dev/toby/internal/agent/resourcelease"
	"petris.dev/toby/internal/agent/resourcelog"
	agentserver "petris.dev/toby/internal/agent/server"
	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/diagnostic"
	ociresourcehandler "petris.dev/toby/internal/oci/resourcehandler"
	"petris.dev/toby/internal/resourcehash"
	"petris.dev/toby/internal/version"

	"go.uber.org/fx"
)

// ClientModule provides the lazy launch-side agent client.
func ClientModule() fx.Option {
	return fx.Module(
		"agent-client",
		fx.Provide(NewClient),
	)
}

// ServerModule provides static agent composition. Dynamic resources
// remain ordinary objects owned by their coordinators rather than Fx graph
// nodes.
func ServerModule() fx.Option {
	return fx.Module(
		"agent-server",
		fx.Provide(
			newResourceBuilder,
			resourcehash.NewService,
			resourcelog.NewService,
			newOCIResourceService,
			newResourceLeaseService,
			newProtocolServer,
			NewService,
		),
	)
}

type ociOpenerResult struct {
	fx.Out

	Opener resourcelease.ResourceOpener `group:"resourceOpeners"`
}

func newOCIResourceService(
	paths config.Paths,
	logs *resourcelog.Service,
	diagnostics *diagnostic.Service,
) (ociOpenerResult, error) {
	service, err := ociresourcehandler.New(
		paths,
		logs,
		diagnostics,
		ociresourcehandler.Options{},
	)
	if err != nil {
		return ociOpenerResult{}, err
	}

	return ociOpenerResult{Opener: service}, nil
}

func newProtocolServer(
	resources *resourcelease.Service,
	logs *resourcelog.Service,
	diagnostics *diagnostic.Service,
) (*agentserver.Service, error) {
	return agentserver.New(
		version.String(),
		resources,
		agentserver.Options{
			ResourceLogs: logs,
			Logger:       diagnostics.Logger("agent.server"),
		},
	)
}

type resourceLeaseParams struct {
	fx.In

	Hashes  *resourcehash.Service
	Openers []resourcelease.ResourceOpener `group:"resourceOpeners"`
}

func newResourceLeaseService(
	params resourceLeaseParams,
) (*resourcelease.Service, error) {
	factories := []func(*resourcehash.Service) (resourcelease.Resolver, error){
		resourcelease.NewOCIResolver,
		resourcelease.NewMCPResolver,
		resourcelease.NewModelsResolver,
	}
	resolvers := make([]resourcelease.Resolver, 0, len(factories))
	for _, factory := range factories {
		resolver, err := factory(params.Hashes)
		if err != nil {
			return nil, err
		}
		resolvers = append(resolvers, resolver)
	}

	return resourcelease.NewService(resolvers, params.Openers)
}
