package contextfiles

import "testing"

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
