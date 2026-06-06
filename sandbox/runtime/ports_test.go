package runtime

import (
	"testing"

	"github.com/moby/moby/api/types/network"
)

func TestResolvePublishedPorts(t *testing.T) {
	exposed, bindings, err := resolvePublishedPorts([]string{"8080:3000", "127.0.0.1:9090:9090/udp", "5000"})
	if err != nil {
		t.Fatalf("resolvePublishedPorts: %v", err)
	}

	tcp3000 := network.MustParsePort("3000/tcp")
	udp9090 := network.MustParsePort("9090/udp")
	tcp5000 := network.MustParsePort("5000/tcp")

	for _, port := range []network.Port{tcp3000, udp9090, tcp5000} {
		if _, ok := exposed[port]; !ok {
			t.Fatalf("exposed ports missing %s: %#v", port, exposed)
		}
	}
	if len(exposed) != 3 {
		t.Fatalf("exposed ports = %#v, want 3 entries", exposed)
	}

	if b := bindings[tcp3000]; len(b) != 1 || b[0].HostPort != "8080" || b[0].HostIP.IsValid() {
		t.Fatalf("3000/tcp binding = %#v", bindings[tcp3000])
	}
	if b := bindings[udp9090]; len(b) != 1 || b[0].HostPort != "9090" || b[0].HostIP.String() != "127.0.0.1" {
		t.Fatalf("9090/udp binding = %#v", bindings[udp9090])
	}
	// A bare container port leaves the host port unset (daemon-assigned).
	if b := bindings[tcp5000]; len(b) != 1 || b[0].HostPort != "" || b[0].HostIP.IsValid() {
		t.Fatalf("5000/tcp binding = %#v", bindings[tcp5000])
	}
}

func TestResolvePublishedPortsEmpty(t *testing.T) {
	exposed, bindings, err := resolvePublishedPorts(nil)
	if err != nil {
		t.Fatalf("resolvePublishedPorts(nil): %v", err)
	}
	if exposed != nil || bindings != nil {
		t.Fatalf("expected nil maps, got exposed=%#v bindings=%#v", exposed, bindings)
	}
}

func TestResolvePublishedPortsErrors(t *testing.T) {
	for _, spec := range []string{
		"",              // empty
		"notaport",      // non-numeric container port
		"8080:bad",      // non-numeric container port
		"1:2:3:4",       // too many fields
		"99999:3000",    // host port out of range
		"nope:8080:300", // invalid host IP
	} {
		if _, _, err := resolvePublishedPorts([]string{spec}); err == nil {
			t.Fatalf("expected error for spec %q", spec)
		}
	}
}
