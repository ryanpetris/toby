package contextfiles

import (
	"path/filepath"
	"reflect"
	"testing"
	"testing/fstest"
)

type recordingSink struct {
	files []File
}

func (s *recordingSink) AddFile(path string, data []byte, mode uint32) error {
	s.files = append(s.files, File{Path: path, Data: append([]byte(nil), data...), Mode: mode})
	return nil
}

func TestEmittingSessionSendsFilesToSink(t *testing.T) {
	service := NewService()
	sink := &recordingSink{}
	session := service.NewEmittingSession("/runtime/toby/context", sink)
	if err := session.AddInstructionBytes("instructions/AGENTS.md", []byte("hi"), 0); err != nil {
		t.Fatal(err)
	}
	if len(sink.files) != 1 {
		t.Fatalf("sink files = %#v", sink.files)
	}
	if sink.files[0].Path != "instructions/AGENTS.md" || string(sink.files[0].Data) != "hi" || sink.files[0].Mode != 0o400 {
		t.Fatalf("sink file = %#v", sink.files[0])
	}
	paths := session.InstructionPaths()
	if len(paths) != 1 || paths[0] != "/runtime/toby/context/instructions/AGENTS.md" {
		t.Fatalf("instruction paths = %#v", paths)
	}
}

func TestBuilderAddBytesValidatesPathDefaultsModeAndClonesData(t *testing.T) {
	builder := NewService().NewBuilder()
	for _, path := range []string{"", ".", "/absolute", "../escape"} {
		if err := builder.AddBytes(path, []byte("bad"), 0); err == nil {
			t.Fatalf("expected path %q to fail", path)
		}
	}

	data := []byte("hello")
	if err := builder.AddBytes("dir/file.txt", data, 0); err != nil {
		t.Fatal(err)
	}
	data[0] = 'H'
	files := builder.Files()
	want := []File{{Path: "dir/file.txt", Data: []byte("hello"), Mode: 0o400}}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("files = %#v, want %#v", files, want)
	}
	files[0].Data[0] = 'X'
	if got := string(builder.Files()[0].Data); got != "hello" {
		t.Fatalf("builder data mutated through Files result: %q", got)
	}
	if err := builder.Close(); err != nil {
		t.Fatal(err)
	}
	if files := builder.Files(); len(files) != 0 {
		t.Fatalf("files after close = %#v", files)
	}
}

func TestSessionInstructionCopiesAndClose(t *testing.T) {
	contextDir := filepath.Join(t.TempDir(), "context")
	session := NewService().NewSession(contextDir)
	data := []byte("instructions")
	if err := session.AddInstructionBytes("instructions/AGENTS.md", data, 0o600); err != nil {
		t.Fatal(err)
	}
	data[0] = 'I'
	paths := session.InstructionPaths()
	wantPath := filepath.Join(contextDir, "instructions", "AGENTS.md")
	if !reflect.DeepEqual(paths, []string{wantPath}) {
		t.Fatalf("paths = %#v, want %#v", paths, []string{wantPath})
	}
	paths[0] = "mutated"
	if got := session.InstructionPaths()[0]; got != wantPath {
		t.Fatalf("instruction path mutated through result: %q", got)
	}
	contents := session.InstructionContents()
	contents[0][0] = 'X'
	if got := string(session.InstructionContents()[0]); got != "instructions" {
		t.Fatalf("instruction content mutated through result: %q", got)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if len(session.InstructionPaths()) != 0 || len(session.InstructionContents()) != 0 || len(session.Files()) != 0 {
		t.Fatalf("session after close: paths=%#v contents=%#v files=%#v", session.InstructionPaths(), session.InstructionContents(), session.Files())
	}
}

func TestAddFSValidationAndInstructionFS(t *testing.T) {
	fsy := fstest.MapFS{"dir/file.txt": {Data: []byte("from fs")}}
	builder := NewService().NewBuilder()
	if err := builder.AddFS("target.txt", nil, "file.txt", 0); err == nil {
		t.Fatal("expected nil fs to fail")
	}
	if err := builder.AddFS("target.txt", fsy, ".", 0); err == nil {
		t.Fatal("expected invalid fs path to fail")
	}
	if err := builder.AddFS("target.txt", fsy, "dir", 0); err == nil {
		t.Fatal("expected directory fs path to fail")
	}
	if err := builder.AddFS("target.txt", fsy, "dir/file.txt", 0o600); err != nil {
		t.Fatal(err)
	}
	if got := builder.Files(); len(got) != 1 || got[0].Path != "target.txt" || string(got[0].Data) != "from fs" || got[0].Mode != 0o600 {
		t.Fatalf("files = %#v", got)
	}

	session := NewService().NewSession(filepath.Join(t.TempDir(), "context"))
	if err := session.AddInstructionFS("instructions/fs.md", fsy, "dir/file.txt", 0); err != nil {
		t.Fatal(err)
	}
	if got := session.InstructionContents(); len(got) != 1 || string(got[0]) != "from fs" {
		t.Fatalf("instruction contents = %#v", got)
	}
}
