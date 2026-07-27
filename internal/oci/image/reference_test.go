package image

// Tests canonical OCI reference normalization shared by clients and storage.

import "testing"

func TestNormalizeReferenceCanonicalizesEquivalentNames(t *testing.T) {
	for _, input := range []string{
		"alpine",
		"alpine:latest",
		"docker.io/library/alpine",
	} {
		got, err := NormalizeReference(input)
		if err != nil {
			t.Fatal(err)
		}
		if got != "docker.io/library/alpine:latest" {
			t.Fatalf("NormalizeReference(%q) = %q", input, got)
		}
	}
}

func TestNormalizeReferencePreservesDigestReferences(t *testing.T) {
	const input = "alpine@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	got, err := NormalizeReference(input)
	if err != nil {
		t.Fatal(err)
	}
	const want = "docker.io/library/alpine@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if got != want {
		t.Fatalf("NormalizeReference() = %q, want %q", got, want)
	}
}
