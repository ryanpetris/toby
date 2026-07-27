package bwrap

// Verifies process-local tool sandbox attachment and stale-clear protection.

import (
	"testing"

	"petris.dev/toby/internal/tools/fake"
)

func TestToolServiceSetDelegateAndClear(t *testing.T) {
	facade := NewToolService()
	first := fake.NewSandbox()
	first.Env["PATH"] = "/first"

	if err := facade.Set(first); err != nil {
		t.Fatal(err)
	}
	if got, ok := facade.Environment("PATH"); !ok || got != "/first" {
		t.Fatalf("delegated environment = %q, %v", got, ok)
	}
	if err := facade.Set(fake.NewSandbox()); err == nil {
		t.Fatal("second attachment was accepted")
	}
	if err := facade.Clear(fake.NewSandbox()); err == nil {
		t.Fatal("stale clear was accepted")
	}
	if err := facade.Clear(first); err != nil {
		t.Fatal(err)
	}
	if err := facade.Set(fake.NewSandbox()); err != nil {
		t.Fatalf("attach after clear: %v", err)
	}
}
