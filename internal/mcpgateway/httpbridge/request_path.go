package httpbridge

// Validates one canonical path-only HTTP request target shared by config,
// gateway acquisition, and bridge endpoint construction.

import (
	"fmt"
	"net/url"
	"path"
	"strings"
	"unicode/utf8"
)

// ValidateRequestPath accepts a clean absolute URL path with valid escapes.
// Queries and fragments are deliberately unsupported so every layer observes
// one identical endpoint identity.
func ValidateRequestPath(value string) error {
	if value == "" || !utf8.ValidString(value) {
		return fmt.Errorf("MCP request path is invalid")
	}
	if strings.ContainsAny(value, "?#") {
		return fmt.Errorf(
			"MCP request path must not contain a query or fragment",
		)
	}
	if !path.IsAbs(value) || path.Clean(value) != value {
		return fmt.Errorf(
			"MCP request path must be a clean absolute path",
		)
	}

	parsed, err := url.ParseRequestURI(value)
	if err != nil {
		return fmt.Errorf("MCP request path is invalid: %w", err)
	}
	if parsed.IsAbs() ||
		parsed.Host != "" ||
		parsed.RawQuery != "" ||
		parsed.ForceQuery {
		return fmt.Errorf("MCP request path must contain only a path")
	}

	return nil
}
