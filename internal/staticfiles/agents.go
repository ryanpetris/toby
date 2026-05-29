package staticfiles

import (
	_ "embed"

	"petris.dev/toby/internal/staticmount"
)

const (
	GitAgentsPath          = "GIT_AGENTS.md"
	ProjectMountAgentsPath = "PROJECT_MOUNT_AGENTS.md"
)

//go:embed GIT_AGENTS.md
var gitAgentsFile []byte

//go:embed PROJECT_MOUNT_AGENTS.md
var projectMountAgentsFile []byte

func AgentFiles(mountableProjects bool) []staticmount.File {
	files := []staticmount.File{{Path: GitAgentsPath, Data: gitAgentsFile, Mode: 0o400}}
	if mountableProjects {
		files = append(files, staticmount.File{Path: ProjectMountAgentsPath, Data: projectMountAgentsFile, Mode: 0o400})
	}
	return files
}
