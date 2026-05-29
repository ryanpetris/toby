package tools

import (
	"context"
	"os"
	"path/filepath"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/tool"
)

func init() { register(newOpenCodeTool) }

type openCodeTool struct {
	tool.Base
	paths config.Paths
}

func newOpenCodeTool(paths config.Paths) tool.Tool {
	return &openCodeTool{
		Base:  tool.Base{Metadata: tool.Metadata{Name: tool.OpenCodeToolName, LaunchHelp: "Launch OpenCode", Dependencies: []string{tool.NpmToolName}, ContextGroups: []string{tool.GroupAI, tool.GroupSystem, tool.GroupVCS}}},
		paths: paths,
	}
}

func (t *openCodeTool) HostInit(ctx context.Context, opts *tool.CommandOptions) error {
	if err := os.MkdirAll(filepath.Join(t.paths.SandboxRoot, ".config", "opencode"), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(t.paths.SandboxRoot, ".config", "opencode-share"), 0o755); err != nil {
		return err
	}
	return nil
}

func (t *openCodeTool) Binds() []tool.Bind {
	return []tool.Bind{
		{HostPath: filepath.Join(t.paths.SandboxRoot, ".config", "opencode"), SandboxPath: filepath.Join(t.paths.Home, ".config", "opencode"), Type: tool.BindRegular},
		{HostPath: filepath.Join(t.paths.SandboxRoot, ".config", "opencode-share"), SandboxPath: filepath.Join(t.paths.Home, ".local", "share", "opencode"), Type: tool.BindRegular},
	}
}

func (t *openCodeTool) SandboxContextSetup(ctx *tool.RunContext) error {
	ctx.Env["OPENCODE_CONFIG_DIR"] = filepath.Join(t.stateHomeDir(), "toby", "static", "opencode")
	return nil
}

func (t *openCodeTool) stateHomeDir() string {
	if t.paths.StateHome != "" {
		return t.paths.StateHome
	}
	return filepath.Join(t.paths.Home, ".local", "state")
}

func (t *openCodeTool) Install(ctx context.Context, run *tool.RunContext, force bool) error {
	if !force {
		exists, err := tool.CommandExists(ctx, run, "opencode")
		if err != nil || exists {
			return err
		}
	}
	return tool.RunCommand(ctx, run.Exec, []string{"npm", "install", "-g", "opencode-ai"}, tool.ExecOptions{})
}

func (t *openCodeTool) Launch(ctx context.Context, run *tool.RunContext) error {
	return tool.RunCommand(ctx, run.Launch, append([]string{"opencode"}, run.Extra...), tool.ExecOptions{})
}
