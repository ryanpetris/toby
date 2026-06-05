package env

import (
	"testing"

	"petris.dev/toby/control"
)

func TestCommandEnvironmentStripsControlEndpoint(t *testing.T) {
	s := New()
	s.Set(control.EnvControlHost, "127.0.0.1:1234")
	s.Set(control.EnvControlToken, "secret")
	s.Set("KEEP", "value")

	env := s.CommandEnvironment()
	if _, ok := env[control.EnvControlHost]; ok {
		t.Fatalf("control host leaked: %#v", env)
	}
	if _, ok := env[control.EnvControlToken]; ok {
		t.Fatalf("control token leaked: %#v", env)
	}
	if env["KEEP"] != "value" {
		t.Fatalf("expected KEEP in env: %#v", env)
	}
}

func TestEnvSetUnsetAndSnapshot(t *testing.T) {
	s := New()
	s.Set("TOBY_TEST_ENV", "value")
	if got, ok := s.Get("TOBY_TEST_ENV"); !ok || got != "value" {
		t.Fatalf("get = %q, %v", got, ok)
	}
	if s.Snapshot()["TOBY_TEST_ENV"] != "value" {
		t.Fatal("snapshot missing value")
	}
	s.Set("TOBY_TEST_ENV", "")
	if _, ok := s.Get("TOBY_TEST_ENV"); ok {
		t.Fatal("expected empty value to unset")
	}
}
