package tools

import (
	"petris.dev/toby/internal/tools/tool"

	"go.uber.org/fx"
)

type planningToolsResult struct {
	fx.Out

	Tools []tool.Tool `group:"toby.tools,flatten"`
}

func newPlanningTools() planningToolsResult {
	metadatas := Metadata()
	result := planningToolsResult{Tools: make([]tool.Tool, 0, len(metadatas))}
	for _, metadata := range metadatas {
		result.Tools = append(result.Tools, tool.Base{Metadata: metadata})
	}
	return result
}

func Metadata() []tool.Metadata {
	items := []tool.Metadata{
		{Name: tool.ExecToolName, LaunchHelp: "Run a command in Toby Sandbox (default: interactive shell).", ContextGroups: []string{tool.GroupAI, tool.GroupUI, tool.GroupSystem, tool.GroupVCS}},
		{Name: tool.NpmToolName, LaunchHelp: "Launch Node Package Manager", ContextGroups: []string{tool.GroupSystem, tool.GroupVCS}},
		{Name: tool.DockerToolName, LaunchHelp: "Launch Docker", ContextGroups: []string{tool.GroupSystem, tool.GroupVCS}},
		{Name: tool.ClaudeToolName, LaunchHelp: "Launch Claude", ContextGroups: []string{tool.GroupAI, tool.GroupSystem, tool.GroupVCS}, Dependencies: []string{tool.NpmToolName}, Priority: 100},
		{Name: tool.CopilotToolName, LaunchHelp: "Launch Copilot", ContextGroups: []string{tool.GroupAI, tool.GroupSystem, tool.GroupVCS}, Dependencies: []string{tool.NpmToolName}, Priority: 100},
		{Name: tool.CodexToolName, LaunchHelp: "Launch Codex", ContextGroups: []string{tool.GroupAI, tool.GroupSystem, tool.GroupVCS}, Dependencies: []string{tool.NpmToolName}, Priority: 100},
		{Name: tool.T3ToolName, LaunchHelp: "Launch T3 Code", ContextGroups: []string{tool.GroupUI, tool.GroupAI, tool.GroupSystem, tool.GroupVCS}, Dependencies: []string{tool.NpmToolName}, Priority: 100},
		{Name: tool.OpenCodeToolName, LaunchHelp: "Launch OpenCode", ContextGroups: []string{tool.GroupAI, tool.GroupSystem, tool.GroupVCS}, Dependencies: []string{tool.NpmToolName}, Priority: 100},
		{Name: tool.UvToolName, LaunchHelp: "Launch UV (Python Package Manager)", ContextGroups: []string{tool.GroupSystem, tool.GroupVCS}},
		{Name: tool.EmdashToolName, LaunchHelp: "Launch Emdash", ContextGroups: []string{tool.GroupUI, tool.GroupAI, tool.GroupSystem, tool.GroupVCS}},
		{Name: tool.GrokToolName, LaunchHelp: "Launch Grok", ContextGroups: []string{tool.GroupAI, tool.GroupSystem, tool.GroupVCS}},
		{Name: tool.SpeckitToolName, LaunchHelp: "Launch Spec Kit", ContextGroups: []string{tool.GroupAI, tool.GroupSystem, tool.GroupVCS}, Dependencies: []string{tool.UvToolName}, Priority: 100},
		{Name: tool.GitHubCliToolName, CLIName: "gh", LaunchHelp: "Launch GitHub CLI", ContextGroups: []string{tool.GroupSystem, tool.GroupVCS}},
		{Name: tool.GitLabCliToolName, CLIName: "glab", LaunchHelp: "Launch GitLab CLI", ContextGroups: []string{tool.GroupSystem, tool.GroupVCS}},
		{Name: tool.ForgejoCliToolName, LaunchHelp: "Launch Forgejo CLI", ContextGroups: []string{tool.GroupSystem, tool.GroupVCS}},
	}
	for i := range items {
		items[i].ContextGroups = append([]string(nil), items[i].ContextGroups...)
		items[i].Dependencies = append([]string(nil), items[i].Dependencies...)
	}
	return items
}
