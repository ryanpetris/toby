package contextfiles

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"testing/fstest"

	"petris.dev/toby/container/layout"
	"petris.dev/toby/sandbox"
)

// fakeSandbox records the files written through the sandbox service. Embedding
// the interface satisfies the full surface; only AddFile is exercised here.
type fakeSandbox struct {
	sandbox.Service
	files []File
}

func (f *fakeSandbox) AddFile(_ context.Context, path string, data []byte, mode uint32) error {
	f.files = append(f.files, File{Path: path, Data: append([]byte(nil), data...), Mode: mode})
	return nil
}

func newServiceWithSandbox() (*Service, *fakeSandbox) {
	service := NewService()
	sink := &fakeSandbox{}
	service.SetSandbox(sink)
	return service, sink
}

func TestAddFileWritesUnderContextRootWithDefaultMode(t *testing.T) {
	service, sink := newServiceWithSandbox()
	target, err := service.AddFile(context.Background(), "dir/file.txt", []byte("hi"), 0)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(layout.Context, "dir", "file.txt")
	if target != want {
		t.Fatalf("target = %q, want %q", target, want)
	}
	if len(sink.files) != 1 || sink.files[0].Path != want || string(sink.files[0].Data) != "hi" || sink.files[0].Mode != 0o644 {
		t.Fatalf("sink file = %#v", sink.files)
	}
	// Non-instruction writes are not tracked as instructions.
	if len(service.InstructionPaths()) != 0 {
		t.Fatalf("instruction paths = %#v", service.InstructionPaths())
	}
}

func TestAddFileRejectsInvalidPaths(t *testing.T) {
	service, _ := newServiceWithSandbox()
	for _, path := range []string{"", ".", "/absolute", "../escape"} {
		if _, err := service.AddFile(context.Background(), path, []byte("bad"), 0); err == nil {
			t.Fatalf("expected path %q to fail", path)
		}
	}
}

func TestAddFileRequiresSandbox(t *testing.T) {
	if _, err := NewService().AddFile(context.Background(), "file.txt", []byte("x"), 0); err == nil {
		t.Fatal("expected missing sandbox to fail")
	}
}

func TestAddInstructionTracksPathsAndContents(t *testing.T) {
	service, sink := newServiceWithSandbox()
	data := []byte("instructions")
	target, err := service.AddInstruction(context.Background(), "instructions/AGENTS.md", data, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	data[0] = 'I' // caller mutation must not bleed into tracked contents.

	want := filepath.Join(layout.Context, "instructions", "AGENTS.md")
	if !reflect.DeepEqual(service.InstructionPaths(), []string{want}) {
		t.Fatalf("instruction paths = %#v, want %#v", service.InstructionPaths(), []string{want})
	}
	if got := service.InstructionContents(); len(got) != 1 || string(got[0]) != "instructions" {
		t.Fatalf("instruction contents = %#v", got)
	}
	if sink.files[0].Mode != 0o600 || sink.files[0].Path != target {
		t.Fatalf("sink file = %#v", sink.files[0])
	}

	// Returned slices are copies — mutating them must not affect the service.
	service.InstructionPaths()[0] = "mutated"
	service.InstructionContents()[0][0] = 'X'
	if got := service.InstructionPaths()[0]; got != want {
		t.Fatalf("instruction path mutated through result: %q", got)
	}
	if got := string(service.InstructionContents()[0]); got != "instructions" {
		t.Fatalf("instruction content mutated through result: %q", got)
	}

	service.Reset()
	if len(service.InstructionPaths()) != 0 || len(service.InstructionContents()) != 0 {
		t.Fatalf("instructions retained after reset")
	}
}

func TestAddInstructionFSValidatesAndReadsFile(t *testing.T) {
	service, _ := newServiceWithSandbox()
	fsy := fstest.MapFS{"dir/file.txt": {Data: []byte("from fs")}}
	for _, name := range []string{"", ".", "missing", "dir"} {
		if _, err := service.AddInstructionFS(context.Background(), "instructions/fs.md", fsy, name, 0); err == nil {
			t.Fatalf("expected fs name %q to fail", name)
		}
	}
	if _, err := service.AddInstructionFS(context.Background(), "instructions/fs.md", nil, "dir/file.txt", 0); err == nil {
		t.Fatal("expected nil fs to fail")
	}
	if _, err := service.AddInstructionFS(context.Background(), "instructions/fs.md", fsy, "dir/file.txt", 0); err != nil {
		t.Fatal(err)
	}
	if got := service.InstructionContents(); len(got) != 1 || string(got[0]) != "from fs" {
		t.Fatalf("instruction contents = %#v", got)
	}
}

func TestRegistrarAddsBytesThroughService(t *testing.T) {
	service, sink := newServiceWithSandbox()
	if err := service.Registrar(context.Background()).AddBytes("config.json", []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(layout.Context, "config.json")
	if len(sink.files) != 1 || sink.files[0].Path != want || string(sink.files[0].Data) != "{}" {
		t.Fatalf("sink files = %#v", sink.files)
	}
}
