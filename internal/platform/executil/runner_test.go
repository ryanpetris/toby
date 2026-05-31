package executil

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"reflect"
	"sort"
	"testing"
)

func TestEnvList(t *testing.T) {
	t.Setenv("TOBY_EXECUTIL_TEST", "1")
	environ := envList(nil)
	found := false
	for _, item := range environ {
		if item == "TOBY_EXECUTIL_TEST=1" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("envList(nil) did not include test env: %#v", environ)
	}

	values := envList(map[string]string{"B": "2", "A": "1"})
	sort.Strings(values)
	want := []string{"A=1", "B=2"}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("envList = %#v, want %#v", values, want)
	}
}

func TestStartErrorCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "not found", err: exec.ErrNotFound, want: 127},
		{name: "not exist", err: os.ErrNotExist, want: 127},
		{name: "permission", err: os.ErrPermission, want: 126},
		{name: "path not exist", err: &os.PathError{Op: "exec", Path: "missing", Err: os.ErrNotExist}, want: 127},
		{name: "path permission", err: &os.PathError{Op: "exec", Path: "denied", Err: os.ErrPermission}, want: 126},
		{name: "eof", err: io.EOF, want: 1},
		{name: "other", err: errors.New("other"), want: 126},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := startErrorCode(tt.err); got != tt.want {
				t.Fatalf("startErrorCode = %d, want %d", got, tt.want)
			}
		})
	}
}
