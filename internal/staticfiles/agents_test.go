package staticfiles

import (
	"bytes"
	"testing"
)

func TestAgentFilesExposeSharedGuidance(t *testing.T) {
	files := AgentFiles(false)
	if len(files) != 1 || files[0].Path != GitAgentsPath {
		t.Fatalf("files = %#v, want git guidance only", files)
	}
	if !bytes.Contains(files[0].Data, []byte("Toby Git")) {
		t.Fatalf("git guidance = %q", files[0].Data)
	}

	files = AgentFiles(true)
	if len(files) != 2 || files[1].Path != ProjectMountAgentsPath {
		t.Fatalf("files = %#v, want project mount guidance", files)
	}
	if !bytes.Contains(files[1].Data, []byte("Toby Project Mounts")) {
		t.Fatalf("project mount guidance = %q", files[1].Data)
	}
}
