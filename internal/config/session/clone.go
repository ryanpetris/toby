package sessionconfig

// Deep-copies resolved session configuration at Holder boundaries so callers
// cannot mutate shared slices, maps, or model payloads.

// Clone returns a deep copy of c.
func (c Config) Clone() Config {
	clone := c
	clone.MCPServers = make([]MCPServer, len(c.MCPServers))
	for i, server := range c.MCPServers {
		clone.MCPServers[i] = server.clone()
	}
	clone.Models = make([]ModelsEndpoint, len(c.Models))
	for i, endpoint := range c.Models {
		clone.Models[i] = endpoint.clone()
	}
	clone.Projects = append([]string(nil), c.Projects...)
	clone.Permissions = cloneStringMap(c.Permissions)
	clone.Instructions.Paths = append([]string(nil), c.Instructions.Paths...)
	clone.Instructions.Contents = make([][]byte, len(c.Instructions.Contents))
	for i, content := range c.Instructions.Contents {
		clone.Instructions.Contents[i] = append([]byte(nil), content...)
	}

	return clone
}

func (p ModelsEndpoint) clone() ModelsEndpoint {
	clone := p
	clone.Models = cloneMap(p.Models)
	return clone
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func cloneMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	clone := make(map[string]any, len(source))
	for key, value := range source {
		clone[key] = cloneValue(value)
	}
	return clone
}

func cloneValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		return cloneMap(value)
	case []any:
		clone := make([]any, len(value))
		for i, item := range value {
			clone[i] = cloneValue(item)
		}
		return clone
	case []string:
		return append([]string(nil), value...)
	case []byte:
		return append([]byte(nil), value...)
	default:
		return value
	}
}
