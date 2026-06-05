package environ

import (
	"reflect"
	"sort"
	"testing"
)

func TestEnvironmentList(t *testing.T) {
	env := Environment{"A": "1", "B": "two=three", "EMPTY": ""}
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
