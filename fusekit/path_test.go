package fusekit

import "testing"

func TestNormalizeVirtualPath(t *testing.T) {
	tests := map[string]string{
		"":                  "/",
		"/":                 "/",
		"foo":               "/foo",
		"/foo//bar/../baz/": "/foo/baz",
		"/foo/./bar":        "/foo/bar",
	}
	for input, want := range tests {
		got, err := NormalizeVirtualPath(input)
		if err != nil {
			t.Fatalf("NormalizeVirtualPath(%q): %v", input, err)
		}
		if got != want {
			t.Fatalf("NormalizeVirtualPath(%q) = %q, want %q", input, got, want)
		}
	}
}
