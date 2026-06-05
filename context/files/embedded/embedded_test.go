package embedded

import (
	"bytes"
	"io/fs"
	"testing"
)

func TestAgentFilesExposeSandboxGuidance(t *testing.T) {
	data, err := fs.ReadFile(AgentFiles(), TobyAgentsPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Toby Sandbox", "git.commit", "toby://docs/git", "mcp.start"} {
		if !bytes.Contains(data, []byte(want)) {
			t.Fatalf("guidance missing %q", want)
		}
	}
}
