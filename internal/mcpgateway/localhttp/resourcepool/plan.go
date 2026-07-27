package resourcepool

// Validates and clones complete resolved process plans without exposing their
// secret-bearing fields.

import (
	"fmt"

	"petris.dev/toby/internal/agent/resource"
	"petris.dev/toby/internal/mcpgateway"
	"petris.dev/toby/internal/mcpgateway/localhttp"
)

func validatePlan(plan Plan) error {
	if plan.Resource.Kind != resource.KindMCPHTTP {
		return fmt.Errorf(
			"local HTTP MCP plan has resource kind %q",
			plan.Resource.Kind,
		)
	}
	if plan.Resource.Transport != resource.TransportHTTP {
		return fmt.Errorf(
			"local HTTP MCP plan has transport %q",
			plan.Resource.Transport,
		)
	}
	if plan.Resource.RunAuthority != resource.RunAuthorityAbsent {
		return fmt.Errorf(
			"local HTTP MCP shared process must not receive run authority",
		)
	}
	if plan.Definition.Image == "" ||
		len(plan.Definition.Command) == 0 {
		return fmt.Errorf("local HTTP MCP launch definition is incomplete")
	}
	for index, item := range plan.Resource.Mounts {
		if item.SourceIdentity.Inode == 0 ||
			item.SourceIdentity.FileType == 0 {
			return fmt.Errorf(
				"local HTTP MCP mount %d has no pinned source identity",
				index,
			)
		}
	}

	return nil
}

func clonePlan(plan Plan) Plan {
	clone := plan
	clone.Resource.Argv = append([]string(nil), plan.Resource.Argv...)
	clone.Resource.Environment = append(
		[]resource.EnvironmentVariable(nil),
		plan.Resource.Environment...,
	)
	clone.Resource.Mounts = append(
		[]resource.Mount(nil),
		plan.Resource.Mounts...,
	)
	clone.Definition = cloneDefinition(plan.Definition)
	return clone
}

func closePlan(plan *Plan) error {
	if plan == nil || plan.Capabilities == nil {
		return nil
	}

	err := plan.Capabilities.Close()
	plan.Capabilities = nil
	return err
}

func cloneDefinition(
	definition localhttp.Definition,
) localhttp.Definition {
	clone := definition
	clone.Command = append([]string(nil), definition.Command...)
	clone.Mounts = append(
		[]mcpgateway.Mount(nil),
		definition.Mounts...,
	)
	clone.Environment = make(
		map[string]string,
		len(definition.Environment),
	)
	for name, value := range definition.Environment {
		clone.Environment[name] = value
	}

	return clone
}
