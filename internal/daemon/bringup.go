// The concrete project.Lifecycle: it builds a per-project fx graph (the shared
// internal/session/graph), starts it, and runs run.BringUp to stand the container up,
// keeping both alive for the project's lifetime. TearDown closes the container and
// stops the graph. The session layer reaches the live run.Container through the Handle.

package daemon

import (
	"context"
	"io"
	"os"

	"petris.dev/toby/config"
	appconfig "petris.dev/toby/internal/config/app"
	"petris.dev/toby/internal/daemon/configwatch"
	"petris.dev/toby/internal/daemon/project"
	"petris.dev/toby/internal/daemon/resource"
	"petris.dev/toby/internal/session/graph"
	"petris.dev/toby/internal/session/run"
	"petris.dev/toby/internal/tools/wiring"
	"petris.dev/toby/tools"

	"go.uber.org/fx"
)

// bringUpRequest carries the first session's launch inputs to the Lifecycle. A
// project's configuration is frozen at the launch that created its container.
type bringUpRequest struct {
	options        *tools.Options
	overrides      appconfig.LaunchOverrides
	requestedTools []string
	primary        string
	profile        string
}

// projectLifecycle builds and tears down per-project containers. It reads the current
// config from the watcher at bring-up time, so a project launched after a config edit
// picks up the change (already-running projects keep their frozen config). The shared
// MCP backend registry is supplied into each per-project graph so sidecar containers
// are shared across projects.
type projectLifecycle struct {
	registry  *tools.Registry
	paths     config.Paths
	config    *configwatch.Watcher
	resources *resource.Registry
	stderr    io.Writer
}

var _ project.Lifecycle = (*projectLifecycle)(nil)

func newLifecycle(registry *tools.Registry, paths config.Paths, watcher *configwatch.Watcher, resources *resource.Registry) project.Lifecycle {
	return &projectLifecycle{registry: registry, paths: paths, config: watcher, resources: resources, stderr: os.Stderr}
}

// BringUp builds the project's fx graph, starts it, and brings the netns unit up.
func (l *projectLifecycle) BringUp(ctx context.Context, key project.Key, req project.Request) (project.Handle, error) {
	r, ok := req.(*bringUpRequest)
	if !ok {
		return nil, errBadBringUpRequest
	}

	effectiveConfig := l.config.Current().WithOverrides(r.overrides)
	selected, err := l.registry.Build(r.requestedTools, r.primary)
	if err != nil {
		return nil, err
	}
	toolModule, err := wiring.SelectedModule(selected.OrderedToolNames())
	if err != nil {
		return nil, err
	}

	var params run.Params
	options := append(graph.Modules(toolModule, l.stderr),
		fx.NopLogger,
		fx.Supply(l.paths, effectiveConfig),
		fx.Supply(l.resources),
		fx.Populate(&params),
	)
	app := fx.New(options...)
	if err := app.Err(); err != nil {
		return nil, fxRootCause(err)
	}
	startCtx, cancel := context.WithTimeout(ctx, app.StartTimeout())
	startErr := app.Start(startCtx)
	cancel()
	if startErr != nil {
		return nil, fxRootCause(startErr)
	}

	container, err := run.BringUp(ctx, params, run.BringUpRequest{
		Options:        r.options,
		RequestedTools: r.requestedTools,
		Primary:        r.primary,
		Profile:        r.profile,
		NetnsName:      netnsName(key, r.profile),
	})
	if err != nil {
		stopApp(app)
		return nil, err
	}
	return &projectHandle{app: app, container: container}, nil
}

// netnsName is the deterministic name of a project+profile's netns container.
func netnsName(key project.Key, profile string) string {
	digest := key.Digest
	if len(digest) > 12 {
		digest = digest[:12]
	}
	return "toby.net." + digest + "." + profile
}

// TearDown closes the container and stops its graph.
func (l *projectLifecycle) TearDown(handle project.Handle) {
	h, ok := handle.(*projectHandle)
	if !ok {
		return
	}
	h.container.Close(context.Background())
	stopApp(h.app)
}

func stopApp(app *fx.App) {
	stopCtx, cancel := context.WithTimeout(context.Background(), app.StopTimeout())
	_ = app.Stop(stopCtx)
	cancel()
}

// projectHandle is a live project: its fx graph plus the brought-up container.
type projectHandle struct {
	app       *fx.App
	container *run.Container
}

var _ project.Handle = (*projectHandle)(nil)

func (h *projectHandle) ContainerID() string       { return h.container.ContainerID() }
func (h *projectHandle) Container() *run.Container { return h.container }
