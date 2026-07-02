package exectool

import (
	"context"
	"reflect"
	"testing"

	"petris.dev/toby/internal/tools/fake"
)

func TestExecLaunchRunsExtraCommand(t *testing.T) {
	sandbox := fake.NewSandbox("/toby/context")
	svc := Provide(sandbox).Service
	got, err := svc.LaunchCommand(context.Background(), []string{"npm", "test"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"npm", "test"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func TestExecLaunchPassesEmptyCommandThrough(t *testing.T) {
	sandbox := fake.NewSandbox("/toby/context")
	svc := Provide(sandbox).Service
	got, err := svc.LaunchCommand(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("argv = %#v, want empty", got)
	}
}
