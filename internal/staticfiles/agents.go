package staticfiles

import (
	"embed"
	"io/fs"
)

const (
	GitAgentsPath          = "GIT_AGENTS.md"
	ProjectMountAgentsPath = "PROJECT_MOUNT_AGENTS.md"
)

//go:embed GIT_AGENTS.md PROJECT_MOUNT_AGENTS.md
var agentFiles embed.FS

func RegisterAgentFiles(registrar Registrar, mountableProjects bool) error {
	if err := registrar.AddFS(GitAgentsPath, agentFiles, GitAgentsPath, 0o400); err != nil {
		return err
	}
	if mountableProjects {
		return registrar.AddFS(ProjectMountAgentsPath, agentFiles, ProjectMountAgentsPath, 0o400)
	}
	return nil
}

func AgentContents(mountableProjects bool) ([][]byte, error) {
	paths := []string{GitAgentsPath}
	if mountableProjects {
		paths = append(paths, ProjectMountAgentsPath)
	}
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
