package providergateway

// Validates provider identity, secret-bearing upstream policy, model values,
// and sandbox-visible loopback capabilities.

import (
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"petris.dev/toby/internal/agent/protocol"
)

const (
	maxProviders        = 64
	maxProviderIDBytes  = 64
	maxDisplayNameRunes = 256
	maxURLBytes         = 4096
	maxHeaders          = 64
	maxHeaderNameBytes  = 128
	maxHeaderValueBytes = 8192
	maxHeaderBytes      = 64 << 10
	maxModels           = 4096
	maxModelDepth       = 32
	maxModelBytes       = protocol.MaxConfigurationBytes
	maxCredentialBytes  = 256
)

var providerIDPattern = regexp.MustCompile(
	`^[a-z][a-z0-9._-]*$`,
)

var reservedProviderHeaders = map[string]struct{}{
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

func (s RequestSpec) validate() error {
	if s.Providers == nil {
		return fmt.Errorf(
			"models gateway providers must be an array",
		)
	}
	if len(s.Providers) > maxProviders {
		return fmt.Errorf(
			"models gateway provider count exceeds %d",
			maxProviders,
		)
	}

	ids := make(map[string]struct{}, len(s.Providers))
	for index, provider := range s.Providers {
		if err := provider.validate(); err != nil {
			return fmt.Errorf("provider %d: %w", index, err)
		}
		if _, duplicate := ids[provider.ID]; duplicate {
			return fmt.Errorf(
				"provider %d has duplicate ID %q",
				index,
				provider.ID,
			)
		}
		ids[provider.ID] = struct{}{}
	}

	return nil
}

func (s ProviderSpec) validate() error {
	if err := validateProviderIdentity(s.ID, s.Type, s.Name); err != nil {
		return err
	}
	if err := validateUpstreamURL(s.URL); err != nil {
		return fmt.Errorf("provider %q URL is invalid: %w", s.ID, err)
	}
	if err := validateProviderHeaders(s.Headers); err != nil {
		return fmt.Errorf(
			"provider %q headers are invalid: %w",
			s.ID,
			err,
		)
	}

	return nil
}

func (c DescriptorConfig) validate() error {
	if c.Providers == nil {
		return fmt.Errorf(
			"models gateway descriptor providers must be an array",
		)
	}
	if len(c.Providers) == 0 {
		return fmt.Errorf(
			"models gateway descriptor must contain at least one provider",
		)
	}
	if len(c.Providers) > maxProviders {
		return fmt.Errorf(
			"models gateway descriptor count exceeds %d",
			maxProviders,
		)
	}

	ids := make(map[string]struct{}, len(c.Providers))
	for index, provider := range c.Providers {
		if err := provider.validate(); err != nil {
			return fmt.Errorf(
				"provider descriptor %d: %w",
				index,
				err,
			)
		}
		if _, duplicate := ids[provider.ID]; duplicate {
			return fmt.Errorf(
				"provider descriptor %d has duplicate ID %q",
				index,
				provider.ID,
			)
		}
		ids[provider.ID] = struct{}{}
	}

	return nil
}

func (d ProviderDescriptor) validate() error {
	if err := validateProviderIdentity(d.ID, d.Type, d.Name); err != nil {
		return err
	}
	if err := validateCapabilityURL(d.URL, d.ID); err != nil {
		return fmt.Errorf("provider %q capability URL is invalid", d.ID)
	}
	if !validCapabilityToken(d.Credential, maxCredentialBytes) {
		return fmt.Errorf(
			"provider %q synthetic credential is invalid",
			d.ID,
		)
	}
	if d.Models == nil {
		return fmt.Errorf(
			"provider %q models must be an object",
			d.ID,
		)
	}
	if err := validateModels(d.Models); err != nil {
		return fmt.Errorf(
			"provider %q models are invalid: %w",
			d.ID,
			err,
		)
	}

	return nil
}

func validateProviderIdentity(
	id string,
	providerType ProviderType,
	name string,
) error {
	if err := validateProviderID(id); err != nil {
		return err
	}
	switch providerType {
	case ProviderOpenAI, ProviderAnthropic:
	default:
		return fmt.Errorf(
			"provider %q has unsupported type %q",
			id,
			providerType,
		)
	}
	if name == "" ||
		!utf8.ValidString(name) ||
		len([]rune(name)) > maxDisplayNameRunes ||
		name != strings.TrimSpace(name) {
		return fmt.Errorf("provider %q display name is invalid", id)
	}
	for _, character := range name {
		if unicode.IsControl(character) {
			return fmt.Errorf(
				"provider %q display name is invalid",
				id,
			)
		}
	}

	return nil
}

func validateProviderID(id string) error {
	if len(id) == 0 ||
		len(id) > maxProviderIDBytes ||
		!providerIDPattern.MatchString(id) {
		return fmt.Errorf("provider ID is invalid")
	}

	return nil
}

func validateUpstreamURL(value string) error {
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

func validateCapabilityURL(value string, providerID string) error {
	if len(value) > maxURLBytes || value != strings.TrimSpace(value) {
		return fmt.Errorf("capability URL text is invalid")
	}

	parsed, err := url.Parse(value)
	if err != nil ||
		parsed.Scheme != "http" ||
		parsed.Hostname() != "127.0.0.1" ||
		parsed.Port() == "" ||
		parsed.User != nil ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" {
		return fmt.Errorf("capability URL is not an isolated loopback URL")
	}
	if net.ParseIP(parsed.Hostname()) == nil {
		return fmt.Errorf("capability host is invalid")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("capability port is invalid")
	}

	segments := strings.Split(strings.TrimPrefix(parsed.EscapedPath(), "/"), "/")
	if len(segments) != 2 ||
		!validCapabilityToken(segments[0], maxCredentialBytes) ||
		segments[1] != providerID {
		return fmt.Errorf("capability path is invalid")
	}

	return nil
}

func validateProviderHeaders(headers map[string]string) error {
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

		if _, reserved := reservedProviderHeaders[name]; reserved ||
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

func validateModels(models map[string]any) error {
	if len(models) > maxModels {
		return fmt.Errorf("model count exceeds %d", maxModels)
	}

	encoded, err := json.Marshal(models)
	if err != nil {
		return fmt.Errorf("encode models")
	}
	if len(encoded) > maxModelBytes {
		return fmt.Errorf(
			"encoded models exceed %d bytes",
			maxModelBytes,
		)
	}
	if err := validateModelValue(models, 0); err != nil {
		return err
	}

	return nil
}

func validateModelValue(value any, depth int) error {
	if depth > maxModelDepth {
		return fmt.Errorf(
			"model value depth exceeds %d",
			maxModelDepth,
		)
	}

	switch typed := value.(type) {
	case nil, bool, string:
		return nil
	case json.Number:
		number, err := typed.Float64()
		if err != nil || math.IsInf(number, 0) ||
			math.IsNaN(number) {
			return fmt.Errorf("model number is invalid")
		}
		return nil
	case float64:
		if math.IsInf(typed, 0) || math.IsNaN(typed) {
			return fmt.Errorf("model number is invalid")
		}
		return nil
	case map[string]any:
		for name, item := range typed {
			if name == "" ||
				!utf8.ValidString(name) ||
				strings.ContainsRune(name, '\x00') {
				return fmt.Errorf("model object key is invalid")
			}
			if err := validateModelValue(item, depth+1); err != nil {
				return err
			}
		}
		return nil
	case []any:
		for _, item := range typed {
			if err := validateModelValue(item, depth+1); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf(
			"model value has unsupported type %T",
			value,
		)
	}
}

func validCapabilityToken(value string, maximum int) bool {
	if value == "" || len(value) > maximum {
		return false
	}
	for _, character := range []byte(value) {
		switch {
		case character >= 'a' && character <= 'z':
		case character >= 'A' && character <= 'Z':
		case character >= '0' && character <= '9':
		case character == '-', character == '_':
		default:
			return false
		}
	}

	return true
}
