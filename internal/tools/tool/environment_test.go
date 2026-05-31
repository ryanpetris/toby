package tool

import (
	"reflect"
	"sort"
	"testing"
)

func TestEnvironmentFromListAndList(t *testing.T) {
	env := EnvironmentFromList([]string{"A=1", "NOPE", "B=two=three", "EMPTY="})
	if env["A"] != "1" || env["B"] != "two=three" || env["EMPTY"] != "" {
		t.Fatalf("env = %#v", env)
	}
	if _, ok := env["NOPE"]; ok {
		t.Fatalf("malformed entry copied: %#v", env)
	}
	list := env.List()
	sort.Strings(list)
	want := []string{"A=1", "B=two=three", "EMPTY="}
	if !reflect.DeepEqual(list, want) {
		t.Fatalf("list = %#v, want %#v", list, want)
	}
}

func TestEnvironmentPrependAndAppendDeduplicateEntries(t *testing.T) {
	env := Environment{"PATH": "/usr/bin:/bin"}
	env.Prepend("PATH", "/custom/bin")
	env.Prepend("PATH", "/usr/bin")
	if got, want := env["PATH"], "/usr/bin:/custom/bin:/bin"; got != want {
		t.Fatalf("prepended PATH = %q, want %q", got, want)
	}
	env.Append("PATH", "/bin")
	env.Append("PATH", "/last")
	if got, want := env["PATH"], "/usr/bin:/custom/bin:/bin:/last"; got != want {
		t.Fatalf("appended PATH = %q, want %q", got, want)
	}

	empty := Environment{}
	empty.Prepend("PATH", "/first")
	if got := empty["PATH"]; got != "/first" {
		t.Fatalf("empty PATH = %q", got)
	}
}
