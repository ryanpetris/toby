package tool

import (
	"context"
	"os"
	"path/filepath"
)

type Simple struct {
	Base
	RootDir             string
	Home                string
	HostSubpath         []string
	SandboxSubpath      []string
	BindType            BindType
	InstallCommand      []string
	InstallCheckCommand string
	SandboxEnv          map[string]string
	LaunchCommand       string
}

func (t *Simple) HostInit(context.Context, *CommandOptions) error {
	if len(t.HostSubpath) == 0 {
		return nil
	}
	return os.MkdirAll(filepath.Join(append([]string{t.RootDir}, t.HostSubpath...)...), 0o755)
}

func (t *Simple) Binds() []Bind {
	if len(t.HostSubpath) == 0 {
		return nil
	}
	sandboxParts := t.SandboxSubpath
	if len(sandboxParts) == 0 {
		sandboxParts = t.HostSubpath
	}
	bindType := t.BindType
	if bindType == "" {
		bindType = BindRegular
	}
	return []Bind{{
		HostPath:    filepath.Join(append([]string{t.RootDir}, t.HostSubpath...)...),
		SandboxPath: filepath.Join(append([]string{t.Home}, sandboxParts...)...),
		Type:        bindType,
	}}
}

func (t *Simple) SandboxContextSetup(ctx *RunContext) error {
	for key, value := range t.SandboxEnv {
		ctx.Env[key] = value
	}
	return nil
}

func (t *Simple) Install(ctx context.Context, run *RunContext, force bool) error {
	if len(t.InstallCommand) == 0 {
		return nil
	}
	check := t.InstallCheckCommand
	if check == "" {
		check = t.Name()
	}
	if !force {
		exists, err := CommandExists(ctx, run, check)
		if err != nil {
			return err
		}
		if exists {
			return nil
		}
	}
	return RunCommand(ctx, run.Exec, t.InstallCommand, ExecOptions{})
}

func (t *Simple) Launch(ctx context.Context, run *RunContext) error {
	command := t.LaunchCommand
	if command == "" {
		command = t.Name()
	}
	argv := append([]string{command}, run.Extra...)
	return RunCommand(ctx, run.Launch, argv, ExecOptions{})
}

func CommandExists(ctx context.Context, run *RunContext, command string) (bool, error) {
	rc, err := run.Exec(ctx, []string{"which", command}, ExecOptions{HideOutput: true})
	if err != nil {
		return false, err
	}
	return rc == 0, nil
}

func RunCommand(ctx context.Context, exec Executor, argv []string, opts ExecOptions) error {
	rc, err := exec(ctx, argv, opts)
	if err != nil {
		return err
	}
	if rc != 0 {
		return exitCode(rc)
	}
	return nil
}

type exitCode int

func (e exitCode) Error() string { return "" }

func (e exitCode) ExitCode() int { return int(e) }
