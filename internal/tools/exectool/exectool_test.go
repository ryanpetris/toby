package exectool

import (
	"context"
	"reflect"
	"testing"

	"petris.dev/toby/internal/tools/tool"
)

func TestExecLaunchRunsExtraCommand(t *testing.T) {
	var got []string
	svc := Provide().Service
	run := &tool.RunContext{
		Extra: []string{"npm", "test"},
		Env:   tool.Environment{"SHELL": "/bin/zsh"},
		Launch: func(_ context.Context, argv []string, _ tool.ExecOptions) (int, error) {
			got = append([]string(nil), argv...)
			return 0, nil
		},
	}
	if err := svc.Launch(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if want := []string{"npm", "test"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func TestExecLaunchPassesEmptyCommandThrough(t *testing.T) {
	var got []string
	svc := Provide().Service
	run := &tool.RunContext{
		Env: tool.Environment{"SHELL": "/bin/zsh"},
		Launch: func(_ context.Context, argv []string, _ tool.ExecOptions) (int, error) {
			got = append([]string(nil), argv...)
			return 0, nil
		},
	}
	if err := svc.Launch(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("argv = %#v, want empty", got)
	}
}
