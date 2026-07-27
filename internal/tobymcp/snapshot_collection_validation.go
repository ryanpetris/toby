package tobymcp

// Validates the bounded project, mount, bind, models, and MCP collections in
// a native session snapshot.

import "fmt"

func validateSessionProjects(projects []SessionProject) error {
	if len(projects) > maxSessionSnapshotItems {
		return fmt.Errorf(
			"session project count exceeds %d",
			maxSessionSnapshotItems,
		)
	}

	seen := make(map[string]struct{}, len(projects))
	for index, project := range projects {
		if err := validateSessionText(
			fmt.Sprintf("session project %d name", index),
			project.Name,
		); err != nil {
			return err
		}
		if _, duplicate := seen[project.Name]; duplicate {
			return fmt.Errorf(
				"session project %d duplicates %q",
				index,
				project.Name,
			)
		}
		seen[project.Name] = struct{}{}
		if err := validateSessionPath(
			fmt.Sprintf("session project %q path", project.Name),
			project.SandboxPath,
		); err != nil {
			return err
		}
	}

	return nil
}

func validateSessionMounts(mounts []SessionMount) error {
	if len(mounts) > maxSessionSnapshotItems {
		return fmt.Errorf(
			"session mount count exceeds %d",
			maxSessionSnapshotItems,
		)
	}

	seen := make(map[string]struct{}, len(mounts))
	for index, mount := range mounts {
		if err := validateSessionText(
			fmt.Sprintf("session mount %d key", index),
			mount.Key,
		); err != nil {
			return err
		}
		if _, duplicate := seen[mount.Key]; duplicate {
			return fmt.Errorf(
				"session mount %d duplicates key %q",
				index,
				mount.Key,
			)
		}
		seen[mount.Key] = struct{}{}
		if err := validateSessionText(
			fmt.Sprintf("session mount %q profile", mount.Key),
			mount.Profile,
		); err != nil {
			return fmt.Errorf(
				"session mount %q profile: %w",
				mount.Key,
				err,
			)
		}
		if err := validateSessionPath(
			fmt.Sprintf("session mount %q target", mount.Key),
			mount.Target,
		); err != nil {
			return err
		}
		if err := validateSessionAccess(
			fmt.Sprintf("session mount %q access", mount.Key),
			mount.Access,
		); err != nil {
			return err
		}
	}

	return nil
}

func validateSessionBinds(binds []SessionBind) error {
	if len(binds) > maxSessionSnapshotItems {
		return fmt.Errorf(
			"session bind count exceeds %d",
			maxSessionSnapshotItems,
		)
	}

	seen := make(map[string]struct{}, len(binds))
	for index, bind := range binds {
		if err := validateSessionPath(
			fmt.Sprintf("session bind %d target", index),
			bind.Target,
		); err != nil {
			return err
		}
		if _, duplicate := seen[bind.Target]; duplicate {
			return fmt.Errorf(
				"session bind %d duplicates target %q",
				index,
				bind.Target,
			)
		}
		seen[bind.Target] = struct{}{}
		if err := validateSessionAccess(
			fmt.Sprintf("session bind %q access", bind.Target),
			bind.Access,
		); err != nil {
			return err
		}
	}

	return nil
}

func validateSessionModels(endpoints []SessionModelsEndpoint) error {
	if len(endpoints) > maxSessionSnapshotItems {
		return fmt.Errorf(
			"session models endpoint count exceeds %d",
			maxSessionSnapshotItems,
		)
	}

	seen := make(map[string]struct{}, len(endpoints))
	for index, endpoint := range endpoints {
		if err := validateSessionText(
			fmt.Sprintf("session models endpoint %d name", index),
			endpoint.Name,
		); err != nil {
			return err
		}
		if _, duplicate := seen[endpoint.Name]; duplicate {
			return fmt.Errorf(
				"session models endpoint %d duplicates %q",
				index,
				endpoint.Name,
			)
		}
		seen[endpoint.Name] = struct{}{}
		if endpoint.Type != "" {
			if err := validateSessionText(
				fmt.Sprintf(
					"session models endpoint %q type",
					endpoint.Name,
				),
				endpoint.Type,
			); err != nil {
				return err
			}
		}
		if err := validateUniqueSessionStrings(
			fmt.Sprintf("session models endpoint %q model", endpoint.Name),
			endpoint.Models,
		); err != nil {
			return err
		}
	}

	return nil
}

func validateSessionMCPs(mcps []SessionMCP) error {
	if len(mcps) > maxSessionSnapshotItems {
		return fmt.Errorf(
			"session MCP count exceeds %d",
			maxSessionSnapshotItems,
		)
	}

	seen := make(map[string]struct{}, len(mcps))
	for index, server := range mcps {
		if err := validateSessionText(
			fmt.Sprintf("session MCP %d name", index),
			server.Name,
		); err != nil {
			return err
		}
		if _, duplicate := seen[server.Name]; duplicate {
			return fmt.Errorf(
				"session MCP %d duplicates %q",
				index,
				server.Name,
			)
		}
		seen[server.Name] = struct{}{}
		if server.Type != "builtin" &&
			server.Type != "local" &&
			server.Type != "remote" {
			return fmt.Errorf(
				"session MCP %q has invalid type %q",
				server.Name,
				server.Type,
			)
		}
		if err := validateSessionMCPStatus(server.Status); err != nil {
			return fmt.Errorf("session MCP %q: %w", server.Name, err)
		}
		if server.Runtime != "" && server.Runtime != "bubblewrap" {
			return fmt.Errorf(
				"session MCP %q has invalid runtime %q",
				server.Name,
				server.Runtime,
			)
		}
		if server.Transport != "stdio" && server.Transport != "http" {
			return fmt.Errorf(
				"session MCP %q has invalid transport %q",
				server.Name,
				server.Transport,
			)
		}
		if server.Scope != "" &&
			server.Scope != "user" &&
			server.Scope != "home" &&
			server.Scope != "project" &&
			server.Scope != "run" {
			return fmt.Errorf(
				"session MCP %q has invalid scope %q",
				server.Name,
				server.Scope,
			)
		}
		if server.Network != "" &&
			server.Network != "host" &&
			server.Network != "private" {
			return fmt.Errorf(
				"session MCP %q has invalid network %q",
				server.Name,
				server.Network,
			)
		}
	}

	return nil
}

func validateSessionMCPStatus(status SessionMCPStatus) error {
	switch status {
	case SessionMCPStatusDisabled,
		SessionMCPStatusConfigured,
		SessionMCPStatusRegistered,
		SessionMCPStatusCold,
		SessionMCPStatusStarting,
		SessionMCPStatusReady,
		SessionMCPStatusRunning,
		SessionMCPStatusIdle,
		SessionMCPStatusStopping,
		SessionMCPStatusExited,
		SessionMCPStatusFailed,
		SessionMCPStatusStopped,
		SessionMCPStatusUnregistered,
		SessionMCPStatusUnknown:
		return nil
	default:
		return fmt.Errorf("status is invalid: %q", status)
	}
}
