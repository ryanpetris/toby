package mcpproxy

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/fx"
)

const FxRuntimeGroup = "toby.mcp.runtimes"

type Runtime interface {
	Name() RuntimeType
	PrepareStart(SidecarSpec) SidecarSpec
	Start(context.Context, SidecarSpec) (*ProcessHandle, error)
	PrepareHTTP(context.Context, SidecarSpec) (string, SidecarSpec, error)
	RuntimeInfo(SidecarSpec, bool) map[string]any
}

type RuntimeResult struct {
	fx.Out

	Runtime Runtime `group:"toby.mcp.runtimes"`
}

type Runner struct {
	runtimes map[RuntimeType]Runtime
}

func NewRunner(runtimes []Runtime) (*Runner, error) {
	runner := &Runner{runtimes: map[RuntimeType]Runtime{}}
	for _, runtime := range runtimes {
		if runtime == nil {
			continue
		}
		name := runtime.Name()
		if strings.TrimSpace(string(name)) == "" {
			return nil, fmt.Errorf("mcp runtime must define a name")
		}
		if _, exists := runner.runtimes[name]; exists {
			return nil, fmt.Errorf("duplicate mcp runtime: %s", name)
		}
		runner.runtimes[name] = runtime
	}
	return runner, nil
}

func (r *Runner) Start(ctx context.Context, spec SidecarSpec) (*ProcessHandle, error) {
	runtime, ok := r.runtime(spec.Runtime)
	if !ok {
		return nil, fmt.Errorf("unsupported MCP runtime %q", spec.Runtime)
	}
	return runtime.Start(ctx, spec)
}

func (r *Runner) PrepareStart(spec SidecarSpec) SidecarSpec {
	runtime, ok := r.runtime(spec.Runtime)
	if !ok {
		return spec
	}
	return runtime.PrepareStart(spec)
}

func (r *Runner) PrepareHTTP(ctx context.Context, spec SidecarSpec) (string, SidecarSpec, error) {
	runtime, ok := r.runtime(spec.Runtime)
	if !ok {
		return "", spec, fmt.Errorf("unsupported MCP runtime %q", spec.Runtime)
	}
	return runtime.PrepareHTTP(ctx, spec)
}

func (r *Runner) RuntimeInfo(spec SidecarSpec, debug bool) map[string]any {
	runtime, ok := r.runtime(spec.Runtime)
	if !ok {
		return nil
	}
	return runtime.RuntimeInfo(spec, debug)
}

func (r *Runner) runtime(name RuntimeType) (Runtime, bool) {
	if r == nil {
		return nil, false
	}
	runtime, ok := r.runtimes[name]
	return runtime, ok
}
