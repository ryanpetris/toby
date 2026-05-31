package tool

import "strings"

type Environment map[string]string

func EnvironmentFromList(values []string) Environment {
	env := make(Environment, len(values))
	for _, item := range values {
		name, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		env[name] = value
	}
	return env
}

func (e Environment) List() []string {
	values := make([]string, 0, len(e))
	for name, value := range e {
		values = append(values, name+"="+value)
	}
	return values
}

func (e Environment) Clone() Environment {
	clone := make(Environment, len(e))
	for name, value := range e {
		clone[name] = value
	}
	return clone
}

func (e Environment) Prepend(name, value string) {
	e.setPathEntry(name, value, true, ":")
}

func (e Environment) Append(name, value string) {
	e.setPathEntry(name, value, false, ":")
}

func (e Environment) setPathEntry(name, value string, atStart bool, separator string) {
	parts := strings.Split(e[name], separator)
	entries := make([]string, 0, len(parts)+1)
	if atStart {
		entries = append(entries, value)
	}
	for _, part := range parts {
		if part == "" || part == value {
			continue
		}
		entries = append(entries, part)
	}
	if !atStart {
		entries = append(entries, value)
	}
	e[name] = strings.Join(entries, separator)
}
