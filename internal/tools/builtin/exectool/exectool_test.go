package exectool

import (
	"context"
	"reflect"
	"testing"

	"petris.dev/toby/internal/tools/fake"
	sandboxapi "petris.dev/toby/sandbox"
)

func TestExecLaunchRunsExtraCommand(t *testing.T) {
	var got []string
	sandbox := fake.NewSandbox("/toby/context")
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ sandboxapi.ExecOptions) (int, error) {
		got = append([]string(nil), argv...)
		return 0, nil
	}
	svc := Provide(sandbox).Service
	if err := svc.Launch(context.Background(), []string{"npm", "test"}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"npm", "test"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func TestExecLaunchPassesEmptyCommandThrough(t *testing.T) {
	var got []string
	sandbox := fake.NewSandbox("/toby/context")
	sandbox.ExecFunc = func(_ context.Context, argv []string, _ sandboxapi.ExecOptions) (int, error) {
		got = append([]string(nil), argv...)
		return 0, nil
	}
	svc := Provide(sandbox).Service
	if err := svc.Launch(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("argv = %#v, want empty", got)
	}
}
