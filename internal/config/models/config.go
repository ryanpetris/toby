package modelsconfig

// Defines defaulting, validation, cloning, and canonical wire fields for one
// models API resource.

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxDisplayNameRunes = 256
	maxURLBytes         = 4096
	maxHeaders          = 64
	maxHeaderNameBytes  = 128
	maxHeaderValueBytes = 8192
	maxHeaderBytes      = 64 << 10
)

var reservedHeaders = map[string]struct{}{
	"Connection":          {},
	"Content-Length":      {},
	"Forwarded":           {},
	"Host":                {},
	"Keep-Alive":          {},
	"Proxy-Authenticate":  {},
	"Proxy-Authorization": {},
	"Proxy-Connection":    {},
	"Te":                  {},
	"Trailer":             {},
	"Transfer-Encoding":   {},
	"Upgrade":             {},
	"Via":                 {},
}

// Protocol identifies a supported models API protocol.
type Protocol string

const (
	// ProtocolAnthropic selects the Anthropic protocol.
	ProtocolAnthropic Protocol = "anthropic"
	// ProtocolOpenAI selects the OpenAI-compatible protocol.
	ProtocolOpenAI Protocol = "openai"
)

// Config is one effective models API resource whose models are discovered
// from the configured upstream.
type Config struct {
	Protocol Protocol          `json:"protocol"`
	Name     string            `json:"name,omitempty"`
	URL      string            `json:"url"`
	Headers  map[string]string `json:"headers,omitempty"`
}

// Normalize trims canonical string fields, clones mutable values, and
// validates the effective configuration.
func Normalize(input Config) (Config, error) {
	result := input.Clone()
	result.Protocol = Protocol(strings.TrimSpace(string(result.Protocol)))
	result.URL = strings.TrimSpace(result.URL)
	if err := result.Validate(); err != nil {
		return Config{}, err
	}

	return result, nil
}

// Validate rejects incomplete and unsupported models resources.
func (c Config) Validate() error {
	switch c.Protocol {
	case ProtocolAnthropic, ProtocolOpenAI:
	default:
		if c.Protocol == "" {
			return fmt.Errorf("models protocol is required")
		}
		return fmt.Errorf(
			"models protocol %q is unsupported",
			c.Protocol,
		)
	}
	if err := validateName(c.Name); err != nil {
		return err
	}
	if err := validateURL(c.URL); err != nil {
		return fmt.Errorf("models URL is invalid: %w", err)
	}
	if err := validateHeaders(c.Headers); err != nil {
		return fmt.Errorf("models headers are invalid: %w", err)
	}

	return nil
}

// Clone returns a detached configuration.
func (c Config) Clone() Config {
	clone := c
	if c.Headers != nil {
		clone.Headers = make(map[string]string, len(c.Headers))
		for key, value := range c.Headers {
			clone.Headers[key] = value
		}
	}

	return clone
}

func validateName(name string) error {
	if name == "" {
		return nil
	}
	if !utf8.ValidString(name) ||
		len([]rune(name)) > maxDisplayNameRunes ||
		name != strings.TrimSpace(name) {
		return fmt.Errorf("models display name is invalid")
	}
	for _, character := range name {
		if unicode.IsControl(character) {
			return fmt.Errorf("models display name is invalid")
		}
	}

	return nil
}

func validateURL(value string) error {
	if value == "" ||
		len(value) > maxURLBytes ||
		value != strings.TrimSpace(value) ||
		!utf8.ValidString(value) {
		return fmt.Errorf("URL text is invalid")
	}

	parsed, err := url.Parse(value)
	if err != nil {
		return fmt.Errorf("parse URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("scheme must be HTTP or HTTPS")
	}
	if parsed.Host == "" ||
		parsed.Hostname() == "" ||
		parsed.User != nil ||
		parsed.Fragment != "" ||
		parsed.RawQuery != "" ||
		parsed.Opaque != "" {
		return fmt.Errorf(
			"URL must have a host and no user information, query, or fragment",
		)
	}
	if parsed.Path != "" &&
		(!strings.HasPrefix(parsed.Path, "/") ||
			strings.ContainsRune(parsed.Path, '\x00')) {
		return fmt.Errorf("base path is invalid")
	}
	if port := parsed.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return fmt.Errorf("port is invalid")
		}
	}

	return nil
}

func validateHeaders(headers map[string]string) error {
	if len(headers) > maxHeaders {
		return fmt.Errorf("header count exceeds %d", maxHeaders)
	}

	canonical := make(map[string]struct{}, len(headers))
	total := 0
	for name, value := range headers {
		if len(name) == 0 ||
			len(name) > maxHeaderNameBytes ||
			name != strings.TrimSpace(name) ||
			!validHeaderName(name) {
			return fmt.Errorf("header name is invalid")
		}
		name = http.CanonicalHeaderKey(name)
		lowerName := strings.ToLower(name)
		if _, duplicate := canonical[lowerName]; duplicate {
			return fmt.Errorf("header %q appears more than once", name)
		}
		canonical[lowerName] = struct{}{}

		if _, reserved := reservedHeaders[name]; reserved ||
			strings.HasPrefix(lowerName, "x-forwarded-") ||
			strings.HasPrefix(lowerName, "x-toby-") {
			return fmt.Errorf("header %q is reserved", name)
		}
		if len(value) > maxHeaderValueBytes ||
			!utf8.ValidString(value) ||
			!validHeaderValue(value) {
			return fmt.Errorf("header %q value is invalid", name)
		}
		total += len(name) + len(value)
		if total > maxHeaderBytes {
			return fmt.Errorf(
				"total header size exceeds %d bytes",
				maxHeaderBytes,
			)
		}
	}

	return nil
}

func validHeaderName(name string) bool {
	for _, character := range []byte(name) {
		switch {
		case character >= 'a' && character <= 'z':
		case character >= 'A' && character <= 'Z':
		case character >= '0' && character <= '9':
		case strings.ContainsRune(
			"!#$%&'*+-.^_`|~",
			rune(character),
		):
		default:
			return false
		}
	}

	return name != ""
}

func validHeaderValue(value string) bool {
	for _, character := range []byte(value) {
		if character == '\t' {
			continue
		}
		if character < 0x20 || character == 0x7f {
			return false
		}
	}

	return true
}
