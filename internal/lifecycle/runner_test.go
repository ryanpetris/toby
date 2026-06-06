package lifecycle

import (
	"context"
	"reflect"
	"testing"

	"petris.dev/toby/internal/status"
	"petris.dev/toby/tools"
)

// recorderTool records which phase methods run, in order, into a shared log.
type recorderTool struct {
	tools.Base
	log *[]string
}

func (r recorderTool) PrepareHost(context.Context, *tools.Options) error {
	*r.log = append(*r.log, "prepare:"+r.Name())
	return nil
}

func (r recorderTool) Install(_ context.Context, force bool) error {
	if force {
		*r.log = append(*r.log, "upgrade:"+r.Name())
	} else {
		*r.log = append(*r.log, "install:"+r.Name())
	}
	return nil
}

func TestRunPhaseRunsHooksThenToolsInOrder(t *testing.T) {
	var log []string
	registry, err := tools.NewRegistry([]tools.Tool{
		recorderTool{Base: tools.Base{Metadata: tools.Metadata{Name: "npm"}}, log: &log},
		recorderTool{Base: tools.Base{Metadata: tools.Metadata{Name: "claude", Dependencies: []string{"npm"}}}, log: &log},
	})
	if err != nil {
		t.Fatal(err)
	}
	set, err := registry.Build([]string{"npm", "claude"}, "claude")
	if err != nil {
		t.Fatal(err)
	}

	runner := NewRunner([]Hook{
		{Phase: PhaseHostPrepare, Name: "early-hook", Priority: -100, Run: func(context.Context, Context) error {
			log = append(log, "hook:early")
			return nil
		}},
	}, status.NewService())

	if err := runner.RunPhase(context.Background(), PhaseHostPrepare, set, Context{}, false); err != nil {
		t.Fatal(err)
	}
	// hooks run first, then tools in topological order (npm before its dependent claude).
	if want := []string{"hook:early", "prepare:npm", "prepare:claude"}; !reflect.DeepEqual(log, want) {
		t.Fatalf("host-prepare order = %#v, want %#v", log, want)
	}

	log = nil
	if err := runner.RunPhase(context.Background(), PhaseInstall, set, Context{}, true); err != nil {
		t.Fatal(err)
	}
	if want := []string{"upgrade:npm", "upgrade:claude"}; !reflect.DeepEqual(log, want) {
		t.Fatalf("install(force) = %#v, want %#v", log, want)
	}
}

func TestRunPhaseSkipsNonParticipatingTools(t *testing.T) {
	// A plain Base tool does not register context files, so the context-files
	// phase must run nothing for it (and not error).
	registry, err := tools.NewRegistry([]tools.Tool{
		tools.Base{Metadata: tools.Metadata{Name: "plain"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	set, err := registry.Build([]string{"plain"}, "")
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(nil, status.NewService())
	if err := runner.RunPhase(context.Background(), PhaseContextFiles, set, Context{}, false); err != nil {
		t.Fatal(err)
	}
}
