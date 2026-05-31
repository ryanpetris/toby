package configfile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

type Format string

const (
	FormatJSON Format = "json"
	FormatYAML Format = "yaml"
)

func Decode(data []byte, format Format, label string) (map[string]any, error) {
	if format == FormatYAML {
		return decodeYAML(data, label)
	}
	return decodeJSONC(data, label)
}

func Merge(dst, src map[string]any) {
	for key, value := range src {
		srcMap, srcOK := value.(map[string]any)
		dstMap, dstOK := dst[key].(map[string]any)
		if srcOK && dstOK {
			Merge(dstMap, srcMap)
			continue
		}
		if isAppendDedupeKey(key) {
			if srcList, srcOK := value.([]any); srcOK {
				if dstList, dstOK := dst[key].([]any); dstOK {
					dst[key] = appendDedupe(key, dstList, srcList)
					continue
				}
			}
		}
		dst[key] = Clone(value)
	}
}

func CloneMap(src map[string]any) map[string]any {
	if src == nil {
		return map[string]any{}
	}
	clone := make(map[string]any, len(src))
	for key, value := range src {
		clone[key] = Clone(value)
	}
	return clone
}

func Clone(value any) any {
	switch v := value.(type) {
	case map[string]any:
		return CloneMap(v)
	case []any:
		clone := make([]any, len(v))
		for i, item := range v {
			clone[i] = Clone(item)
		}
		return clone
	default:
		return value
	}
}

func decodeJSONC(data []byte, label string) (map[string]any, error) {
	cleaned, err := stripJSONC(data)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", label, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(cleaned))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("parse %s: %w", label, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", label, err)
		}
		return nil, fmt.Errorf("parse %s: multiple JSON values", label)
	}
	config, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s must be a JSON object", label)
	}
	return config, nil
}

func decodeYAML(data []byte, label string) (map[string]any, error) {
	var value any
	if err := yaml.Unmarshal(data, &value); err != nil {
		return nil, fmt.Errorf("parse %s: %w", label, err)
	}
	if value == nil {
		return map[string]any{}, nil
	}
	normalized, err := normalizeYAML(value, label)
	if err != nil {
		return nil, err
	}
	config, ok := normalized.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s must be a YAML object", label)
	}
	return config, nil
}

func normalizeYAML(value any, label string) (any, error) {
	switch v := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(v))
		for key, item := range v {
			normalized, err := normalizeYAML(item, label)
			if err != nil {
				return nil, err
			}
			result[key] = normalized
		}
		return result, nil
	case map[any]any:
		result := make(map[string]any, len(v))
		for key, item := range v {
			stringKey, ok := key.(string)
			if !ok {
				return nil, fmt.Errorf("%s contains non-string YAML key: %v", label, key)
			}
			normalized, err := normalizeYAML(item, label)
			if err != nil {
				return nil, err
			}
			result[stringKey] = normalized
		}
		return result, nil
	case []any:
		result := make([]any, len(v))
		for i, item := range v {
			normalized, err := normalizeYAML(item, label)
			if err != nil {
				return nil, err
			}
			result[i] = normalized
		}
		return result, nil
	default:
		return value, nil
	}
}

func stripJSONC(data []byte) ([]byte, error) {
	withoutComments, err := stripComments(data)
	if err != nil {
		return nil, err
	}
	return stripTrailingCommas(withoutComments), nil
}

func stripComments(data []byte) ([]byte, error) {
	var out bytes.Buffer
	inString := false
	escaped := false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if inString {
			out.WriteByte(c)
			if escaped {
				escaped = false
				continue
			}
			if c == '\\' {
				escaped = true
				continue
			}
			if c == '"' {
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			out.WriteByte(c)
			continue
		}
		if c == '/' && i+1 < len(data) {
			next := data[i+1]
			if next == '/' {
				i += 2
				for i < len(data) && data[i] != '\n' && data[i] != '\r' {
					i++
				}
				if i < len(data) {
					out.WriteByte(data[i])
				}
				continue
			}
			if next == '*' {
				i += 2
				closed := false
				for i+1 < len(data) {
					if data[i] == '*' && data[i+1] == '/' {
						i++
						closed = true
						break
					}
					if data[i] == '\n' || data[i] == '\r' {
						out.WriteByte(data[i])
					}
					i++
				}
				if !closed {
					return nil, errors.New("unterminated block comment")
				}
				continue
			}
		}
		out.WriteByte(c)
	}
	if inString {
		return nil, errors.New("unterminated string")
	}
	return out.Bytes(), nil
}

func stripTrailingCommas(data []byte) []byte {
	var out bytes.Buffer
	inString := false
	escaped := false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if inString {
			out.WriteByte(c)
			if escaped {
				escaped = false
				continue
			}
			if c == '\\' {
				escaped = true
				continue
			}
			if c == '"' {
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			out.WriteByte(c)
			continue
		}
		if c == ',' {
			j := i + 1
			for j < len(data) && strings.ContainsRune(" \t\r\n", rune(data[j])) {
				j++
			}
			if j < len(data) && (data[j] == '}' || data[j] == ']') {
				continue
			}
		}
		out.WriteByte(c)
	}
	return out.Bytes()
}

func isAppendDedupeKey(key string) bool {
	return key == "instructions" || key == "plugin"
}

func appendDedupe(key string, dst, src []any) []any {
	if key == "plugin" {
		return appendDedupeLastWins(dst, src)
	}
	result := make([]any, 0, len(dst)+len(src))
	seen := map[string]bool{}
	for _, item := range append(dst, src...) {
		if s, ok := item.(string); ok {
			if seen[s] {
				continue
			}
			seen[s] = true
		}
		result = append(result, Clone(item))
	}
	return result
}

func appendDedupeLastWins(dst, src []any) []any {
	combined := append(append([]any{}, dst...), src...)
	winning := map[string]int{}
	for i := len(combined) - 1; i >= 0; i-- {
		s, ok := combined[i].(string)
		if !ok {
			continue
		}
		canonical := canonicalPluginName(s)
		if _, exists := winning[canonical]; !exists {
			winning[canonical] = i
		}
	}
	result := make([]any, 0, len(combined))
	for i, item := range combined {
		if s, ok := item.(string); ok {
			if winning[canonicalPluginName(s)] != i {
				continue
			}
		}
		result = append(result, Clone(item))
	}
	return result
}

func canonicalPluginName(specifier string) string {
	hasNPMPrefix := strings.HasPrefix(specifier, "npm:")
	remainder := specifier
	if hasNPMPrefix {
		remainder = strings.TrimPrefix(specifier, "npm:")
	}
	if remainder == "" {
		return specifier
	}
	if strings.HasPrefix(remainder, "@") {
		slash := strings.Index(remainder, "/")
		if slash == -1 {
			return specifier
		}
		afterSlash := remainder[slash+1:]
		versionAt := strings.Index(afterSlash, "@")
		if versionAt == -1 {
			return specifier
		}
		canonical := remainder[:slash+1+versionAt]
		if hasNPMPrefix {
			return "npm:" + canonical
		}
		return canonical
	}
	versionAt := strings.Index(remainder, "@")
	if versionAt == -1 {
		return specifier
	}
	canonical := remainder[:versionAt]
	if hasNPMPrefix {
		return "npm:" + canonical
	}
	return canonical
}
