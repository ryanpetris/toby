// Package sessionconfig is the sandbox-safe, pre-resolved configuration handed to
// the agent tools. A host-side resolver does all the privileged work once per
// launch — registering MCP servers and models endpoints behind protected
// connectors, resolving secrets, and fetching models. Tools render this Config
// into their native formats; they never see raw host configuration or real
// credentials.
package sessionconfig

// Config is the resolved per-run configuration exposed to tools. Every field is
// sandbox-safe: endpoints and commands refer only to run-scoped connectors, and
// no upstream API keys, headers, resolved secret environment, or real upstream
// URLs are present. Models credentials are synthetic run-scoped capabilities.
type Config struct {
	// MCPServers are the MCP servers the agent can reach, including Toby's own
	// built-in server. Each carries a sandbox-safe transport descriptor.
	MCPServers []MCPServer
	// Models are the resolved models endpoints, each with a proxied base URL and
	// an already-fetched model list.
	Models []ModelsEndpoint
	// Projects are the exact sandbox-visible roots selected for the launch.
	Projects []string
	// Permissions maps a path pattern to an access mode ("allow"/"deny").
	Permissions map[string]string
	// Instructions are the rendered instruction files written for the run.
	Instructions Instructions
}

// ModelsEndpoint is one resolved models endpoint: its id, kind
// ("anthropic"/"openai"), display name, proxied base URL, synthetic credential,
// and resolved model map.
type ModelsEndpoint struct {
	ID         string
	Type       string
	Name       string
	URL        string
	Credential string
	Models     map[string]any
}

// Instructions are the instruction files contributed for the launch, available
// both as sandbox paths (for tools that reference files) and as raw contents
// (for tools that concatenate them inline).
type Instructions struct {
	Paths    []string
	Contents [][]byte
}
