package sandbox

import (
	"errors"
	"reflect"
	"testing"

	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/tools/helpers"
	"petris.dev/toby/internal/tools/tool"
)

func TestCommandExitResultReturnsExitCodeErrors(t *testing.T) {
	code, err := commandExitResult(control.CommandExitParams{ExitCode: 7})
	var coded interface{ ExitCode() int }
	if code != 7 || !errors.As(err, &coded) || coded.ExitCode() != 7 || err.Error() != "" {
		t.Fatalf("code = %d, err = %#v, coded = %#v", code, err, coded)
	}
}

func TestCommandExitResultReturnsCommandErrors(t *testing.T) {
	code, err := commandExitResult(control.CommandExitParams{ExitCode: 127, Error: "not found"})
	var coded interface{ ExitCode() int }
	if code != 127 || !errors.As(err, &coded) || coded.ExitCode() != 127 || err.Error() != "not found" {
		t.Fatalf("code = %d, err = %#v, coded = %#v", code, err, coded)
	}
}

func TestCommandExitResultSuccess(t *testing.T) {
	code, err := commandExitResult(control.CommandExitParams{})
	if code != 0 || err != nil {
		t.Fatalf("code = %d, err = %v", code, err)
	}
}

func TestServiceTracksBindsUntilSandboxStarts(t *testing.T) {
	svc := newService()
	bind := tool.Bind{HostPath: "/host/state", Target: helpers.HomeTarget(".state")}
	if err := svc.AddBind(bind); err != nil {
		t.Fatal(err)
	}
	if err := svc.AddBind(bind); err != nil {
		t.Fatal(err)
	}
	if got, want := svc.StartBinds(), []tool.Bind{bind}; !reflect.DeepEqual(got, want) {
		t.Fatalf("binds = %#v, want %#v", got, want)
	}
	if err := svc.AddBind(tool.Bind{HostPath: "/host/other", Target: helpers.HomeTarget(".other")}); err == nil {
		t.Fatal("expected AddBind after start to fail")
	}
	svc.Prepare(nil)
	if err := svc.AddBind(tool.Bind{HostPath: "/host/other", Target: helpers.HomeTarget(".other")}); err != nil {
		t.Fatal(err)
	}
}
