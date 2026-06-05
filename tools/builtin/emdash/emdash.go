// Package emdash provides the Emdash AI agent tool, installed into the sandbox
// from a bundled install script.
package emdash

import (
	"context"
	"path/filepath"
	"petris.dev/toby/container/layout"

	contextfiles "petris.dev/toby/context/files"
	"petris.dev/toby/sandbox"
	"petris.dev/toby/tools"
	"petris.dev/toby/tools/helpers"

	"go.uber.org/fx"
)

const appImageURL = "https://github.com/generalaction/emdash/releases/latest/download/emdash-x86_64.AppImage"

var Module = fx.Module("tools.emdash", fx.Provide(Provide))

// Name is this tool's canonical identifier.
const Name = "emdash"

// Meta is this tool's declarative identity.
var Meta = tools.Metadata{
	Name:          Name,
	LaunchHelp:    "Launch Emdash",
	Group:         tools.GroupUI,
	ContextGroups: []string{tools.GroupUI, tools.GroupAI, tools.GroupSystem, tools.GroupVCS},
}

const emdashInstallPath = "emdash/install.sh"

type Result struct {
	fx.Out

	Service tools.Tool `group:"tools"`
}

type Params struct {
	fx.In

	Sandbox      sandbox.Service
	ContextFiles *contextfiles.Service
}

func Provide(params Params) Result {
	svc := &emdashTool{Base: tools.Base{Metadata: Meta}, sandbox: params.Sandbox, contextFiles: params.ContextFiles}
	return Result{Service: svc}
}

type emdashTool struct {
	tools.Base
	sandbox      sandbox.Service
	contextFiles *contextfiles.Service
}

var _ tools.Tool = (*emdashTool)(nil)

func (t *emdashTool) RegisterContextFiles(ctx context.Context, _ tools.ContextOptions) error {
	data, err := emdashFiles.ReadFile("resources/install.sh")
	if err != nil {
		return err
	}
	_, err = t.contextFiles.AddFile(ctx, emdashInstallPath, data, 0o755)
	return err
}

func (t *emdashTool) Install(ctx context.Context, force bool) error {
	if !force {
		exists, err := helpers.CommandExists(ctx, t.sandbox.Exec, sandbox.ExecOptions{HideOutput: true}, "emdash")
		if err != nil || exists {
			return err
		}
	}

	_, err := t.sandbox.Exec(ctx, []string{t.contextPath(emdashInstallPath), appImageURL}, sandbox.ExecOptions{})
	return err
}

func (t *emdashTool) contextPath(path string) string {
	return filepath.Join(layout.Context, filepath.FromSlash(path))
}

func (t *emdashTool) Launch(ctx context.Context, extra []string) error {
	_, err := t.sandbox.Exec(ctx, append([]string{"emdash"}, extra...), sandbox.ExecOptions{Foreground: true})
	return err
}
