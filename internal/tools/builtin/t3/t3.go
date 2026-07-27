// Package t3 provides the T3 Code agent tool, installed via npm and launched
// through a wrapper script.
package t3

import (
	"context"
	pathpkg "path"

	"petris.dev/toby/internal/runtimeassets"
	"petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/tools"
	"petris.dev/toby/internal/tools/builtin/npm"
	"petris.dev/toby/internal/tools/kit"
	"petris.dev/toby/internal/tools/runtimepath"
)

// Name is this tool's canonical identifier.
const Name = "t3"

// Meta is this tool's declarative identity. It runs after npm via its dependency.
var Meta = tools.Metadata{
	Name:          Name,
	DisplayName:   "T3 Code",
	LaunchHelp:    "Launch T3 Code",
	Group:         tools.GroupAI,
	ContextGroups: []string{tools.GroupUI, tools.GroupAI, tools.GroupSystem, tools.GroupVCS},
	Dependencies:  []string{npm.Name},
}

const t3WrapperPath = "t3/t3-wrapper.sh"

// provide constructs the tool implementation.
func provide(params params) result {
	simple := kit.NewSimple(
		params.Sandbox,
		tools.Base{Metadata: Meta},
		nil,
		[]string{"npm", "install", "-g", "t3"},
		map[string]string{"T3CODE_NO_BROWSER": "1"},
	)
	simple.InstallCheckCommand = "t3"
	svc := &t3Tool{Simple: simple}
	return result{Service: svc}
}

type t3Tool struct {
	*kit.Simple
}

var _ tools.Tool = (*t3Tool)(nil)
var _ runtimeassets.Contributor = (*t3Tool)(nil)

func (t *t3Tool) RuntimeAssets() ([]runtimeassets.Asset, error) {
	data, err := t3Files.ReadFile("resources/t3-wrapper.sh")
	if err != nil {
		return nil, err
	}

	return []runtimeassets.Asset{{
		Target: pathpkg.Join(layout.Runtime, t3WrapperPath),
		Data:   data,
		Mode:   0o755,
	}}, nil
}

func (t *t3Tool) Launch(ctx context.Context, extra []string) error {
	wrapper, err := runtimepath.Resolve(t3WrapperPath)
	if err != nil {
		return err
	}
	_, err = t.Sandbox.Exec(ctx, append([]string{wrapper}, extra...), sandbox.ExecOptions{Foreground: true})
	return err
}
