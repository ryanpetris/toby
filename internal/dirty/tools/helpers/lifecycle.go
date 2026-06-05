package helpers

import (
	"context"

	"petris.dev/toby/tools"
)

// These wrappers used to deduplicate per-session phase execution. The lifecycle
// runner now invokes each tool's phase method exactly once, so they are simple
// passthroughs kept to avoid churning every tool call site in one change.
// TODO: inline these at the call sites and delete this file.

func HostInitOnce(_ *tools.Options, _ string, fn func() error) error { return fn() }

func SandboxContextSetupOnce(_ context.Context, _ string, fn func() error) error { return fn() }

func RegisterContextFilesOnce(_ context.Context, _ string, fn func() error) error { return fn() }

func SandboxInitOnce(_ context.Context, _ string, fn func() error) error { return fn() }

func InstallOnce(_ context.Context, _ string, fn func() error) error { return fn() }

func UpgradeOnce(_ context.Context, _ string, fn func() error) error { return fn() }
