package tools

import (
	"context"
	"fmt"
	"path/filepath"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/shellquote"
	"petris.dev/toby/internal/tool"
)

func init() {
	register(newPrintTool)
	register(newNpmTool)
	register(newDockerTool)
	register(newClaudeTool)
	register(newCopilotTool)
	register(newCodexTool)
	register(newT3Tool)
}

type printTool struct{ tool.Base }

func newPrintTool() tool.Tool {
	return &printTool{Base: tool.Base{Metadata: tool.Metadata{Name: tool.PrintToolName}}}
}

func (t *printTool) SandboxContextSetup(ctx *tool.RunContext) error {
	ctx.Exec = func(context.Context, []string, tool.ExecOptions) (int, error) { return 0, nil }
	ctx.Launch = func(_ context.Context, argv []string, _ tool.ExecOptions) (int, error) {
		fmt.Println(shellquote.Join(ctx.Sandbox.BuildCommand(argv, ctx.Toolset)))
		return 0, nil
	}
	return nil
}

type npmTool struct {
	tool.Base
	paths config.Paths
}

func newNpmTool(paths config.Paths) tool.Tool {
	return &npmTool{
		Base:  simpleBase(tool.NpmToolName, "Launch Node Package Manager", tool.GroupSystem, tool.GroupVCS),
		paths: paths,
	}
}

func (t *npmTool) PathEntries() []string {
	return []string{filepath.Join(t.paths.Home, ".local", "npm-global", "bin")}
}

func (t *npmTool) SandboxContextSetup(ctx *tool.RunContext) error {
	prefix := filepath.Join(t.paths.Home, ".local", "npm-global")
	cache := filepath.Join(t.paths.Home, ".cache", "npm")
	ctx.Env["NPM_CONFIG_PREFIX"] = prefix
	ctx.Env["npm_config_prefix"] = prefix
	ctx.Env["NPM_CONFIG_CACHE"] = cache
	ctx.Env["npm_config_cache"] = cache
	return nil
}

func (t *npmTool) SandboxInit(ctx context.Context, run *tool.RunContext) error {
	script := `if ! command -v npm >/dev/null 2>&1; then printf "npm is not available inside the sandbox\n" >&2; exit 127; fi; if [ -d "$NPM_CONFIG_PREFIX/bin" ] && [ -d "$NPM_CONFIG_PREFIX/lib/node_modules" ]; then exit 0; fi; mkdir -p "$NPM_CONFIG_PREFIX/bin" "$NPM_CONFIG_PREFIX/lib/node_modules" "$NPM_CONFIG_CACHE"`
	return tool.RunCommand(ctx, run.Exec, []string{"bash", "-lc", script}, tool.ExecOptions{})
}

func (t *npmTool) Launch(ctx context.Context, run *tool.RunContext) error {
	return tool.RunCommand(ctx, run.Launch, append([]string{"npm"}, run.Extra...), tool.ExecOptions{})
}

type dockerTool struct {
	tool.Base
	paths config.Paths
}

func newDockerTool(paths config.Paths) tool.Tool {
	return &dockerTool{
		Base:  simpleBase(tool.DockerToolName, "Launch Docker", tool.GroupSystem, tool.GroupVCS),
		paths: paths,
	}
}

func (t *dockerTool) Binds() []tool.Bind {
	return []tool.Bind{
		{HostPath: filepath.Join(t.paths.Home, ".docker"), SandboxPath: filepath.Join(t.paths.Home, ".docker"), Type: tool.BindReadOnly, Optional: true},
		{HostPath: "/var/run/docker.sock", SandboxPath: "/var/run/docker.sock", Type: tool.BindDev, Optional: true},
	}
}

func (t *dockerTool) Launch(ctx context.Context, run *tool.RunContext) error {
	return tool.RunCommand(ctx, run.Launch, append([]string{"docker"}, run.Extra...), tool.ExecOptions{})
}

func newClaudeTool(paths config.Paths) tool.Tool {
	return simpleTool(
		paths,
		simpleBaseWithDeps(tool.ClaudeToolName, "Launch Claude", []string{tool.NpmToolName}, tool.GroupSystem, tool.GroupVCS),
		[]string{".config", "claude"},
		[]string{".config", "claude"},
		[]string{"npm", "install", "-g", "@anthropic-ai/claude-code"},
		map[string]string{"CLAUDE_CONFIG_DIR": filepath.Join(paths.Home, ".config", "claude")},
	)
}

func newCopilotTool(paths config.Paths) tool.Tool {
	return simpleTool(
		paths,
		simpleBaseWithDeps(tool.CopilotToolName, "Launch Copilot", []string{tool.NpmToolName}, tool.GroupSystem, tool.GroupVCS),
		[]string{".config", "copilot"},
		[]string{".copilot"},
		[]string{"npm", "install", "-g", "@github/copilot"},
		nil,
	)
}

func newCodexTool(paths config.Paths) tool.Tool {
	return simpleTool(
		paths,
		simpleBaseWithDeps(tool.CodexToolName, "Launch Codex", []string{tool.NpmToolName}, tool.GroupSystem, tool.GroupVCS),
		[]string{".config", "codex"},
		[]string{".codex"},
		[]string{"npm", "install", "-g", "@openai/codex"},
		nil,
	)
}

func newT3Tool(paths config.Paths) tool.Tool {
	return simpleTool(
		paths,
		simpleBaseWithDeps(tool.T3ToolName, "Launch T3 Code", []string{tool.NpmToolName}, tool.GroupUI, tool.GroupAI, tool.GroupSystem, tool.GroupVCS),
		nil,
		nil,
		[]string{"npm", "install", "-g", "t3"},
		map[string]string{"T3CODE_NO_BROWSER": "1"},
	)
}
