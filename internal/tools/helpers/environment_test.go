package helpers

import "testing"

func TestEnvironmentFromList(t *testing.T) {
	env := EnvironmentFromList([]string{"A=1", "NOPE", "B=two=three", "EMPTY="})
	if env["A"] != "1" || env["B"] != "two=three" || env["EMPTY"] != "" {
		t.Fatalf("env = %#v", env)
	}
	if _, ok := env["NOPE"]; ok {
		t.Fatalf("malformed entry copied: %#v", env)
	}
}
