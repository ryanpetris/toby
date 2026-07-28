//go:build linux

package bwrap

// Verifies bounded, sealed confidential argument payloads and replay-safe
// descriptor duplication.

import (
	"io"
	"slices"
	"strings"
	"testing"
)

func TestDuplicateInvocationRecreatesConfidentialArgumentsAtOffsetZero(
	t *testing.T,
) {
	plan := validSidecarPlan()
	plan.Environment[0].Value = "duplicate-secret-sentinel"
	invocation, err := RenderSidecar(plan, validSidecarSources(t, plan))
	if err != nil {
		t.Fatal(err)
	}
	defer invocation.Close()

	payload := invocation.ExtraFiles[invocation.confidentialArgumentsIndex]
	if _, err := payload.Seek(0, io.SeekEnd); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		duplicate, err := duplicateInvocation(invocation)
		if err != nil {
			t.Fatal(err)
		}

		payloadIndex := duplicate.confidentialArgumentsIndex
		offset, err := duplicate.ExtraFiles[payloadIndex].Seek(
			0,
			io.SeekCurrent,
		)
		if err != nil {
			_ = duplicate.Close()
			t.Fatal(err)
		}
		if offset != 0 {
			_ = duplicate.Close()
			t.Fatalf("duplicated argument payload offset = %d, want 0", offset)
		}

		args, err := invocationArguments(duplicate)
		closeErr := duplicate.Close()
		if err != nil {
			t.Fatal(err)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
		if !strings.Contains(
			strings.Join(args, "\x00"),
			"duplicate-secret-sentinel",
		) {
			t.Fatalf("duplicated argument payload omits secret: %q", args)
		}
	}
}

func TestRenderSidecarBoundsConfidentialArgumentPayload(t *testing.T) {
	plan := validSidecarPlan()
	plan.Environment[0].Value = strings.Repeat(
		"x",
		maxConfidentialArgumentPayloadSize,
	)

	invocation, err := RenderSidecar(
		plan,
		validSidecarSources(t, plan),
	)
	if invocation != nil {
		_ = invocation.Close()
		t.Fatal("oversized confidential arguments returned an invocation")
	}
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized confidential argument result = %v", err)
	}
}

func TestEncodeConfidentialArgumentsBoundsCount(t *testing.T) {
	args := make([]string, maxConfidentialArgumentCount+1)

	payload, err := encodeConfidentialArguments(args)
	if payload != nil {
		clear(payload)
		t.Fatal("excess confidential argument count returned a payload")
	}
	if err == nil || !strings.Contains(err.Error(), "count") {
		t.Fatalf("excess confidential argument count result = %v", err)
	}
}

func TestConfidentialOptionsKeepOnlyPublicCommandInOuterArguments(
	t *testing.T,
) {
	invocation := &Invocation{Mode: ExecutionNonInteractive}
	if err := invocation.setConfidentialOptions(
		[]string{
			"--clearenv",
			"--setenv", "SECRET", "protected-value",
		},
		[]string{"/usr/bin/service", "serve"},
	); err != nil {
		t.Fatal(err)
	}
	defer invocation.Close()

	if got, want := invocation.Args, []string{
		"--args", "3", "--", "/usr/bin/service", "serve",
	}; !slices.Equal(got, want) {
		t.Fatalf("outer arguments = %q, want %q", got, want)
	}
	if strings.Contains(
		strings.Join(invocation.Args, "\x00"),
		"protected-value",
	) {
		t.Fatal("outer arguments expose the protected option value")
	}

	args, err := invocationArguments(invocation)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := args, []string{
		"--clearenv",
		"--setenv", "SECRET", "protected-value",
		"--",
		"/usr/bin/service", "serve",
	}; !slices.Equal(got, want) {
		t.Fatalf("expanded arguments = %q, want %q", got, want)
	}
}
