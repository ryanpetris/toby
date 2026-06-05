package docker

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"petris.dev/toby/config"
	"petris.dev/toby/container/engine"
	"petris.dev/toby/internal/dirty/sandbox"
	"petris.dev/toby/tools"

	"github.com/testcontainers/testcontainers-go"
	"go.uber.org/fx"
)

// DefaultImage is the image used when no image or build is configured.
const DefaultImage = "mcr.microsoft.com/devcontainers/javascript-node:24-bookworm"

type runtime struct {
	paths      config.Paths
	containers *engine.Service

	availableOnce sync.Once
	available     error
}

var _ sandbox.Runtime = (*runtime)(nil)

type instance struct {
	sandbox.BaseInstance
	containers    *engine.Service
	image         string
	build         tools.Build
	containerName string

	mu           sync.Mutex
	primed       bool
	runContainer testcontainers.Container
}

// Module registers the docker sandbox runtime into the sandbox runtime group.
func Module() fx.Option {
	return fx.Module(
		"sandbox.docker",
		fx.Provide(fx.Annotate(
			newRuntime,
			fx.ResultTags(`group:"`+sandbox.FxRuntimeGroup+`"`),
		)),
	)
}

func newRuntime(paths config.Paths, containers *engine.Service) sandbox.Runtime {
	return &runtime{paths: paths, containers: containers}
}

func (r *runtime) Name() string { return sandbox.RuntimeDocker }

func (r *runtime) Priority() int { return 0 }

// Available pings the Docker daemon once and caches the result. A clear error is
// returned when no daemon is reachable (a Docker-compatible daemon is required).
func (r *runtime) Available() error {
	r.availableOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		r.available = r.containers.Ping(ctx)
	})
	return r.available
}

func (r *runtime) NewInstance(spec sandbox.Spec) (sandbox.Instance, error) {
	image := spec.Image
	if image == "" && !spec.Build.IsSet() {
		image = DefaultImage
	}
	base, err := sandbox.NewBaseInstance(sandbox.BaseInstanceParams{
		Label:    spec.Label,
		Workdir:  spec.Workdir,
		Projects: spec.Projects,
	})
	if err != nil {
		return nil, err
	}
	return &instance{
		BaseInstance:  base,
		containers:    r.containers,
		image:         image,
		build:         spec.Build,
		containerName: fmt.Sprintf("toby-%d-%d", os.Getpid(), time.Now().UnixNano()),
	}, nil
}

func (s *instance) RuntimeInfo(debug bool) sandbox.RuntimeInfo {
	info := map[string]any{
		"image": s.image,
	}
	if debug && s.build.IsSet() {
		info["build"] = map[string]any{"context": s.build.Context, "dockerfile": s.build.Dockerfile}
	}
	if debug && s.containerName != "" {
		info["container"] = map[string]any{
			"baseName": s.containerName,
			"prime":    s.phaseContainerName("prime", true),
			"setup":    s.phaseContainerName("setup", true),
			"run":      s.phaseContainerName("run", true),
		}
	}
	if debug {
		var tracked []map[string]any
		for _, c := range s.containers.GetSnapshot() {
			if c.Kind != engine.KindSandbox {
				continue
			}
			tracked = append(tracked, map[string]any{
				"id":      c.ID,
				"phase":   c.Phase,
				"image":   c.Image,
				"network": c.Network,
			})
		}
		if len(tracked) > 0 {
			info["containers"] = tracked
		}
	}
	return sandbox.RuntimeInfo{Runtime: sandbox.RuntimeDocker, Info: info}
}

// Cleanup defensively terminates the long-lived Run container if it is still
// tracked (e.g. an early return skipped the normal teardown).
func (s *instance) Cleanup() error {
	s.mu.Lock()
	ctr := s.runContainer
	s.runContainer = nil
	s.mu.Unlock()
	if ctr != nil {
		_ = s.containers.Terminate(context.Background(), ctr)
	}
	return nil
}

func (s *instance) phaseContainerName(phase string, debug bool) string {
	if !debug || phase == "" {
		return s.containerName
	}
	return s.containerName + "-" + phase
}

func (s *instance) meta(phase phaseKind, class engine.DaemonClass) engine.Meta {
	return engine.Meta{
		Label:   s.Label(),
		Kind:    engine.KindSandbox,
		Phase:   phase.String(),
		Image:   s.image,
		Network: networkLabel(class, phase),
	}
}

func stdinIsTerminal() bool { return isCharDevice(os.Stdin) }

func stdoutIsTerminal() bool { return isCharDevice(os.Stdout) }

func isCharDevice(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
