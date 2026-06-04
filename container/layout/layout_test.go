package layout

import "testing"

func TestExpand(t *testing.T) {
	cases := map[string]string{
		"~":                    Home,
		"~/.config/opencode":   Home + "/.config/opencode",
		"/toby/workspace/demo": "/toby/workspace/demo",
		"/var/run/docker.sock": "/var/run/docker.sock",
		"":                     "",
	}
	for in, want := range cases {
		if got := Expand(in); got != want {
			t.Fatalf("Expand(%q) = %q, want %q", in, got, want)
		}
	}
}
