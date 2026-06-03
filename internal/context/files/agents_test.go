package contextfiles

import (
	"bytes"
	"testing"
)

func TestAgentFilesExposeSandboxGuidance(t *testing.T) {
	builder := NewService().NewBuilder()
	if err := RegisterAgentFiles(builder); err != nil {
		t.Fatal(err)
	}
	files := builder.Files()
	if len(files) != 1 || files[0].Path != TobyAgentsPath {
		t.Fatalf("files = %#v, want Toby sandbox guidance only", files)
	}
	for _, want := range []string{"Toby Sandbox", "git.commit", "toby://docs/git", "mcp.start"} {
		if !bytes.Contains(files[0].Data, []byte(want)) {
			t.Fatalf("guidance missing %q", want)
		}
	}
}

func TestAgentContentsReturnsInstruction(t *testing.T) {
	contents, err := AgentContents()
	if err != nil {
		t.Fatal(err)
	}
	if len(contents) != 1 || !bytes.Contains(contents[0], []byte("Toby Sandbox")) {
		t.Fatalf("contents = %#v, want Toby sandbox guidance", contents)
	}
}
