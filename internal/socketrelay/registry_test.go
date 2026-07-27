package socketrelay

// Exercises registration validation before host access or runtime mutation.

import (
	"strings"
	"testing"

	"petris.dev/toby/internal/sandbox/layout"
)

func TestNewRegistryRejectsInvalidRequests(t *testing.T) {
	t.Parallel()

	valid := Request{
		HostSocket:    "/var/run/docker.sock",
		SandboxSocket: layout.Runtime + "/docker.sock",
	}
	tests := []struct {
		name     string
		requests []Request
		want     string
	}{
		{
			name: "relative host",
			requests: []Request{{
				HostSocket:    "docker.sock",
				SandboxSocket: valid.SandboxSocket,
			}},
			want: "clean absolute",
		},
		{
			name: "runtime root",
			requests: []Request{{
				HostSocket:    valid.HostSocket,
				SandboxSocket: layout.Runtime,
			}},
			want: "strictly beneath",
		},
		{
			name: "outside runtime",
			requests: []Request{{
				HostSocket:    valid.HostSocket,
				SandboxSocket: "/var/run/docker.sock",
			}},
			want: "strictly beneath",
		},
		{
			name:     "duplicate target",
			requests: []Request{valid, valid},
			want:     "overlapping",
		},
		{
			name: "overlapping targets",
			requests: []Request{
				valid,
				{
					HostSocket:    "/run/other.sock",
					SandboxSocket: valid.SandboxSocket + "/nested",
				},
			},
			want: "overlapping",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := NewRegistry(test.requests)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf(
					"NewRegistry() error = %v, want containing %q",
					err,
					test.want,
				)
			}
		})
	}
}
