package toolwiring

import (
	"petris.dev/toby/tools"

	"go.uber.org/fx"
)

type planningToolsResult struct {
	fx.Out

	Tools []tools.Tool `group:"toby.tools,flatten"`
}

func newPlanningTools() planningToolsResult {
	metadatas := Metadata()
	result := planningToolsResult{Tools: make([]tools.Tool, 0, len(metadatas))}
	for _, metadata := range metadatas {
		result.Tools = append(result.Tools, tools.Base{Metadata: metadata})
	}
	return result
}

func Metadata() []tools.Metadata {
	items := []tools.Metadata{
		{Name: tools.ExecToolName, LaunchHelp: "Run a command in Toby Sandbox (default: interactive shell).", ContextGroups: []string{tools.GroupCommand, tools.GroupAI, tools.GroupUI, tools.GroupSystem, tools.GroupVCS}},
		{Name: tools.NpmToolName, LaunchHelp: "Launch Node Package Manager", ContextGroups: []string{tools.GroupSystem, tools.GroupVCS}},
		{Name: tools.DockerToolName, LaunchHelp: "Launch Docker", ContextGroups: []string{tools.GroupSystem, tools.GroupVCS}},
		{Name: tools.ClaudeToolName, LaunchHelp: "Launch Claude", ContextGroups: []string{tools.GroupAI, tools.GroupSystem, tools.GroupVCS}, Dependencies: []string{tools.NpmToolName}, Priority: 100},
		{Name: tools.CopilotToolName, LaunchHelp: "Launch Copilot", ContextGroups: []string{tools.GroupAI, tools.GroupSystem, tools.GroupVCS}, Dependencies: []string{tools.NpmToolName}, Priority: 100},
		{Name: tools.CodexToolName, LaunchHelp: "Launch Codex", ContextGroups: []string{tools.GroupAI, tools.GroupSystem, tools.GroupVCS}, Dependencies: []string{tools.NpmToolName}, Priority: 100},
		{Name: tools.T3ToolName, LaunchHelp: "Launch T3 Code", ContextGroups: []string{tools.GroupUI, tools.GroupAI, tools.GroupSystem, tools.GroupVCS}, Dependencies: []string{tools.NpmToolName}, Priority: 100},
		{Name: tools.OpenCodeToolName, LaunchHelp: "Launch OpenCode", ContextGroups: []string{tools.GroupAI, tools.GroupSystem, tools.GroupVCS}, Dependencies: []string{tools.NpmToolName}, Priority: 100},
		{Name: tools.UvToolName, LaunchHelp: "Launch UV (Python Package Manager)", ContextGroups: []string{tools.GroupSystem, tools.GroupVCS}},
		{Name: tools.EmdashToolName, LaunchHelp: "Launch Emdash", ContextGroups: []string{tools.GroupUI, tools.GroupAI, tools.GroupSystem, tools.GroupVCS}},
		{Name: tools.GrokToolName, LaunchHelp: "Launch Grok", ContextGroups: []string{tools.GroupAI, tools.GroupSystem, tools.GroupVCS}},
		{Name: tools.SpeckitToolName, LaunchHelp: "Launch Spec Kit", ContextGroups: []string{tools.GroupAI, tools.GroupSystem, tools.GroupVCS}, Dependencies: []string{tools.UvToolName}, Priority: 100},
		{Name: tools.GitHubCliToolName, CLIName: "gh", LaunchHelp: "Launch GitHub CLI", ContextGroups: []string{tools.GroupVCS, tools.GroupSystem}},
		{Name: tools.GitLabCliToolName, CLIName: "glab", LaunchHelp: "Launch GitLab CLI", ContextGroups: []string{tools.GroupVCS, tools.GroupSystem}},
		{Name: tools.ForgejoCliToolName, LaunchHelp: "Launch Forgejo CLI", ContextGroups: []string{tools.GroupVCS, tools.GroupSystem}},
	}
	for i := range items {
		// A tool's primary group is the first group it declares (matching the
		// toolutil.Base convention used by the real tools).
		if len(items[i].ContextGroups) > 0 {
			items[i].Group = items[i].ContextGroups[0]
		}
		items[i].ContextGroups = append([]string(nil), items[i].ContextGroups...)
		items[i].Dependencies = append([]string(nil), items[i].Dependencies...)
	}
	return items
}
