package env

// Wire shapes for the env.* methods, carried in the JSON-RPC params/result fields.

type GetResult struct {
	Environment map[string]string `json:"environment" jsonschema:"sandbox manager environment variables"`
}

type SetParams struct {
	Name  string `json:"name" jsonschema:"environment variable name"`
	Value string `json:"value" jsonschema:"environment variable value; empty unsets the variable"`
}
