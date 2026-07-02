// fx wiring for the daemon process: it collects the control capabilities into a
// router, builds the Service over the injected transport listener, and runs the
// accept loop for the process lifetime.

package daemon

import (
	"context"
	"fmt"
	"os"
	"time"

	"petris.dev/toby/container/engine"
	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/daemon/home"
	"petris.dev/toby/internal/daemon/project"
	"petris.dev/toby/internal/daemon/resource"
	"petris.dev/toby/internal/daemon/transport"
	"petris.dev/toby/internal/version"

	"go.uber.org/fx"
)

// Options are the daemon's runtime knobs, parsed from the `toby daemon` command line.
type Options struct {
	// NoIdleShutdown keeps the daemon alive regardless of idleness (set when running
	// under a supervisor such as systemd, which owns the lifecycle).
	NoIdleShutdown bool
}

// Module wires the daemon. The caller supplies Options and includes a transport
// module (unix socket or WebSocket) that provides transport.Listener.
func Module() fx.Option {
	return fx.Module("daemon",
		// The daemon-root engine, shared MCP backend registry, and shared per-profile
		// home containers: one set of sidecars and one home per profile, across projects.
		engine.Module(),
		resource.Module(),
		home.Module(),
		fx.Provide(
			newStartedAt,
			newVersion,
			newLifecycle,
			newProjectRegistry,
			newProjectLister,
			asCapability(newMethods),
			asCapability(newSessionMethods),
			newRouter,
			newDaemonService,
		),
		fx.Invoke(runService),
	)
}

// routerParams collects every daemon control capability contributed to the group.
type routerParams struct {
	fx.In

	Handlers []control.Capability `group:"daemon.handlers"`
}

func newRouter(params routerParams) (*control.Router, error) {
	return control.NewRouter(params.Handlers)
}

// asCapability annotates a constructor so its result joins the daemon.handlers group
// as a control.Capability.
func asCapability(constructor any) any {
	return fx.Annotate(constructor,
		fx.As(new(control.Capability)),
		fx.ResultTags(`group:"daemon.handlers"`),
	)
}

func newStartedAt() time.Time { return time.Now() }

func newVersion() string { return version.String() }

// defaultProjectIdleTimeout is how long a project container stays warm after its last
// session exits before the registry tears it down.
const defaultProjectIdleTimeout = 15 * time.Minute

// newProjectRegistry builds the project registry. onEmpty triggers the daemon's idle
// auto-shutdown unless --no-idle-shutdown keeps it supervised.
func newProjectRegistry(lifecycle project.Lifecycle, options Options, shutdowner fx.Shutdowner) *project.Registry {
	onEmpty := func() {
		if options.NoIdleShutdown {
			return
		}
		_ = shutdowner.Shutdown()
	}
	return project.NewRegistry(lifecycle, defaultProjectIdleTimeout, onEmpty)
}

// newProjectLister adapts the registry to the daemon.status project source.
func newProjectLister(registry *project.Registry) projectLister { return registry }

// newDaemonService builds the Service with a fatal handler that reports an unusable
// endpoint and shuts the daemon down rather than letting the accept loop spin.
func newDaemonService(listener transport.Listener, router *control.Router, shutdowner fx.Shutdowner) *Service {
	fatal := func(err error) {
		fmt.Fprintf(os.Stderr, "toby daemon: %v\n", err)
		_ = shutdowner.Shutdown()
	}
	return newService(listener, router, fatal)
}

// runService starts the accept loop on start and tears the daemon down on stop:
// it stops accepting, then tears down every live netns unit and shared home
// container so none is orphaned when the daemon exits.
func runService(lc fx.Lifecycle, service *Service, registry *project.Registry, homeReg *home.Registry) {
	ctx, cancel := context.WithCancel(context.Background())
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			go service.serve(ctx)
			return nil
		},
		OnStop: func(context.Context) error {
			cancel()
			err := service.close()
			registry.Shutdown()
			homeReg.Shutdown()
			return err
		},
	})
}
