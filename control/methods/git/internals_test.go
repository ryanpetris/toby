package git

import (
	"errors"
	"syscall"
	"testing"

	"petris.dev/toby/control"
)

func TestValidateRepositoryName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "trim", input: " foo/bar ", want: "foo/bar"},
		{name: "empty", input: " ", wantErr: true},
		{name: "absolute", input: "/foo", wantErr: true},
		{name: "empty segment", input: "foo//bar", wantErr: true},
		{name: "dot", input: "foo/./bar", wantErr: true},
		{name: "dot dot", input: "foo/../bar", wantErr: true},
		{name: "nul", input: "foo\x00bar", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := validateRepositoryName(tt.input)
			if tt.wantErr {
				if !errors.Is(err, syscall.EINVAL) {
					t.Fatalf("err = %v, want EINVAL", err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("validateRepositoryName = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestValidateGitArgument(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "trim", input: " main ", want: "main"},
		{name: "empty", input: " ", wantErr: true},
		{name: "flag", input: "-bad", wantErr: true},
		{name: "nul", input: "main\x00", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := validateGitArgument(tt.input)
			if tt.wantErr {
				if !errors.Is(err, syscall.EINVAL) {
					t.Fatalf("err = %v, want EINVAL", err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("validateGitArgument = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestGitErrorMappings(t *testing.T) {
	notVisible := wrapProjectNotVisible(errors.New("nope"))
	if !errors.Is(errnoFor(notVisible), syscall.EACCES) || rpcErrorCode(notVisible) != control.CodeProjectNotVisible {
		t.Fatalf("not visible mappings = %v, %d", errnoFor(notVisible), rpcErrorCode(notVisible))
	}
	if !errors.Is(errnoFor(syscall.EINVAL), syscall.EINVAL) || rpcErrorCode(syscall.EINVAL) != control.CodeInvalidParams {
		t.Fatalf("EINVAL mappings = %v, %d", errnoFor(syscall.EINVAL), rpcErrorCode(syscall.EINVAL))
	}
	if rpcErrorCode(syscall.ENOSYS) != control.CodeInternalError {
		t.Fatalf("ENOSYS rpc code = %d", rpcErrorCode(syscall.ENOSYS))
	}
	if !errors.Is(errnoFor(errors.New("other")), syscall.EIO) || rpcErrorCode(errors.New("other")) != control.CodeInternalError {
		t.Fatal("unexpected generic error mappings")
	}
}
