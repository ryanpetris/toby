package lifecycle

// Tests lifecycle phase ordering, status, and error propagation.

import (
	"context"
	"reflect"
	"testing"

	"petris.dev/toby/internal/status"
	"petris.dev/toby/internal/tools"
)

// recorderTool records which phase methods run, in order, into a shared log.
type recorderTool struct {
	tools.Base
	log *[]string
}

type statusRecorderTool struct {
	tools.Base
	hasOperation *bool
}

func (t statusRecorderTool) Install(ctx context.Context, _ bool) error {
	*t.hasOperation = CommandOperation(ctx) != nil
	return nil
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

func TestRunPhaseRunsToolsInOrder(t *testing.T) {
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

	runner := NewRunner(status.NewService(nil))

	if err := runner.RunPhase(context.Background(), PhaseHostPrepare, set, Context{}, false); err != nil {
		t.Fatal(err)
	}
	if want := []string{"prepare:npm", "prepare:claude"}; !reflect.DeepEqual(log, want) {
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

func TestRunPhaseSuppliesLifecycleCommandOperation(t *testing.T) {
	var hasCommandOperation bool
	registry, err := tools.NewRegistry([]tools.Tool{
		statusRecorderTool{
			Base: tools.Base{
				Metadata: tools.Metadata{
					Name:        "opencode",
					DisplayName: "OpenCode",
				},
			},
			hasOperation: &hasCommandOperation,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	set, err := registry.Build([]string{"opencode"}, "opencode")
	if err != nil {
		t.Fatal(err)
	}

	runner := NewRunner(status.NewService(nil))
	if err := runner.RunPhase(
		t.Context(),
		PhaseInstall,
		set,
		Context{},
		false,
	); err != nil {
		t.Fatal(err)
	}
	if !hasCommandOperation {
		t.Fatal("command operation is unavailable")
	}
}
