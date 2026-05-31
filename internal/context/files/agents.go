package contextfiles

import (
	"embed"
	"io/fs"
)

const (
	GitAgentsPath = "GIT_AGENTS.md"
)

//go:embed GIT_AGENTS.md
var agentFiles embed.FS

func RegisterAgentFiles(registrar Registrar) error {
	return registrar.AddFS(GitAgentsPath, agentFiles, GitAgentsPath, 0o400)
}

func RegisterAgentInstructions(session *Session) error {
	return session.AddInstructionFS(GitAgentsPath, agentFiles, GitAgentsPath, 0o400)
}

func AgentContents() ([][]byte, error) {
	paths := []string{GitAgentsPath}
	contents := make([][]byte, 0, len(paths))
	for _, path := range paths {
		data, err := fs.ReadFile(agentFiles, path)
		if err != nil {
			return nil, err
		}
		contents = append(contents, data)
	}
	return contents, nil
}
