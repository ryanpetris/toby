// Package environ models a process environment as a name→value map, with helpers
// for rendering it as a KEY=VALUE list and for prepending/appending entries to
// separator-joined path-style variables (e.g. PATH) without duplicates.
package environ

import "strings"

// Environment is a set of environment variables keyed by name.
type Environment map[string]string

// List renders the environment as KEY=VALUE strings in unspecified order.
func (e Environment) List() []string {
	values := make([]string, 0, len(e))
	for name, value := range e {
		values = append(values, name+"="+value)
	}
	return values
}

// Clone returns a shallow copy of the environment.
func (e Environment) Clone() Environment {
	clone := make(Environment, len(e))
	for name, value := range e {
		clone[name] = value
	}
	return clone
}

// Prepend inserts value at the front of the colon-separated variable name,
// removing any existing occurrence so it does not appear twice.
func (e Environment) Prepend(name, value string) {
	e.setPathEntry(name, value, true, ":")
}

// Append adds value to the end of the colon-separated variable name, removing
// any existing occurrence so it does not appear twice.
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
