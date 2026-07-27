package httpbridge

// Exercises canonical path-only MCP endpoint validation.

import "testing"

func TestValidateRequestPath(t *testing.T) {
	t.Parallel()

	for _, valid := range []string{
		"/mcp",
		"/v1/mcp",
		"/mcp%20server",
	} {
		if err := ValidateRequestPath(valid); err != nil {
			t.Errorf("ValidateRequestPath(%q): %v", valid, err)
		}
	}

	for _, invalid := range []string{
		"",
		"mcp",
		"//mcp",
		"/mcp/../other",
		"/mcp?token=value",
		"/mcp#fragment",
		"/mcp%zz",
		"/mcp\nother",
		string([]byte{'/', 0xff}),
	} {
		if err := ValidateRequestPath(invalid); err == nil {
			t.Errorf(
				"ValidateRequestPath(%q) accepted an invalid target",
				invalid,
			)
		}
	}
}
