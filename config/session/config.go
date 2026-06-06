// Package sessionconfig is the sandbox-safe, pre-resolved configuration handed to
// the agent tools. A host-side resolver does all the privileged work once per
// launch — registering MCP servers and provider endpoints behind the proxy,
// resolving secrets, fetching models — and produces a Config of proxied URLs and
// non-secret metadata only. Tools render this Config into their own config-file
// formats; they never see the raw host configuration, the proxy, or any
// credential.
package sessionconfig

// Config is the resolved per-launch configuration exposed to tools. Every field
// is sandbox-safe: URLs point at the host proxy (never at a real upstream), and
// no secrets (API keys, headers, resolved env) are present.
type Config struct {
	// MCPServers are the MCP servers the agent can reach, including Toby's own
	// built-in server. Each carries a proxied URL; none is run inside the
	// execution sandbox.
	MCPServers []MCPServer
	// Providers are the resolved LLM providers (OpenCode only consumer today),
	// each with a proxied base URL and an already-fetched model list.
	Providers []Provider
	// Permissions maps a path pattern to an access mode ("allow"/"deny").
	Permissions map[string]string
	// Instructions are the rendered instruction files written into the sandbox.
	Instructions Instructions
}

// MCPServer is one MCP server entry: a name and the proxied URL the agent
// connects to. Connection details and secrets are resolved away behind the URL
// and never appear here; disabled servers are omitted entirely.
type MCPServer struct {
	Name string
	URL  string
}

// Provider is one resolved LLM provider: its id, kind ("anthropic"/"openai"),
// display name, the proxied base URL, and the resolved model map.
type Provider struct {
	ID     string
	Type   string
	Name   string
	URL    string
	Models map[string]any
}

// Instructions are the instruction files contributed for the launch, available
// both as sandbox paths (for tools that reference files) and as raw contents
// (for tools that concatenate them inline).
type Instructions struct {
	Paths    []string
	Contents [][]byte
}
