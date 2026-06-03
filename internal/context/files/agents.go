package contextfiles

import (
	"embed"
	"io/fs"
)

const (
	TobyAgentsPath = "TOBY_AGENTS.md"
)

//go:embed TOBY_AGENTS.md
var agentFiles embed.FS

func AgentFiles() fs.FS { return agentFiles }

func RegisterAgentFiles(registrar Registrar) error {
	return registrar.AddFS(TobyAgentsPath, agentFiles, TobyAgentsPath, 0o644)
}

func AgentContents() ([][]byte, error) {
	paths := []string{TobyAgentsPath}
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
