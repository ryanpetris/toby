package shellquote

import "strings"

func Join(argv []string) string {
	parts := make([]string, len(argv))
	for i, arg := range argv {
		parts[i] = Quote(arg)
	}
	return strings.Join(parts, " ")
}

func Quote(arg string) string {
	if arg == "" {
		return "''"
	}
	if strings.IndexFunc(arg, needsQuote) == -1 {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", "'\\''") + "'"
}

func needsQuote(r rune) bool {
	if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
		return false
	}
	switch r {
	case '_', '-', '.', '/', ':', ',', '+', '=', '@', '%':
		return false
	default:
		return true
	}
}
