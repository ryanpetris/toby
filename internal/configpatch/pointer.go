package configpatch

// Parses and formats RFC 6901 JSON Pointers.

import (
	"fmt"
	"strconv"
	"strings"
)

func parsePointer(pointer string) ([]string, error) {
	if pointer == "" || pointer[0] != '/' {
		return nil, fmt.Errorf("json pointer %q must start with /", pointer)
	}
	if pointer == "/" {
		return []string{""}, nil
	}

	tokens := strings.Split(pointer[1:], "/")
	for index, token := range tokens {
		tokens[index] = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
	}
	return tokens, nil
}

func formatPointer(tokens []string) string {
	var builder strings.Builder
	for _, token := range tokens {
		builder.WriteByte('/')
		token = strings.ReplaceAll(token, "~", "~0")
		token = strings.ReplaceAll(token, "/", "~1")
		builder.WriteString(token)
	}
	return builder.String()
}

func parseArrayIndex(token string, length int) (int, error) {
	if token == "-" {
		return 0, fmt.Errorf("json pointer '-' is not a concrete array index")
	}
	if token == "" || (len(token) > 1 && token[0] == '0') {
		return 0, fmt.Errorf("json pointer array index %q is invalid", token)
	}
	for _, character := range token {
		if character < '0' || character > '9' {
			return 0, fmt.Errorf("json pointer array index %q is invalid", token)
		}
	}
	index, err := strconv.Atoi(token)
	if err != nil {
		return 0, fmt.Errorf("json pointer array index %q is invalid", token)
	}
	if index < 0 || index >= length {
		return 0, fmt.Errorf("json pointer array index %d is out of range", index)
	}
	return index, nil
}
