//go:build linux

package bwrap

// Verifies per-run source retention, command serialization, and exact overlay
// ownership without executing Bubblewrap.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"petris.dev/toby/internal/sandbox/mount"
)

type recordingProcessExecutor struct {
	mu         sync.Mutex
	modes      []ExecutionMode
	retries    []bool
	streams    []ProcessIO
	active     int
	maxActive  int
	delay      time.Duration
	exitCode   int
	executeErr error
}

var _ ProcessExecutor = (*recordingProcessExecutor)(nil)

type overlapGate struct {
	entered chan string
	release chan struct{}
}

type gatedProcessExecutor struct {
	name string
	gate *overlapGate
}

var _ ProcessExecutor = (*gatedProcessExecutor)(nil)

func (e *gatedProcessExecutor) Execute(
	_ context.Context,
	_ *Invocation,
	_ ProcessIO,
) (int, error) {
	e.gate.entered <- e.name
	<-e.gate.release
	return 0, nil
}

func (e *recordingProcessExecutor) Execute(
	_ context.Context,
	invocation *Invocation,
	streams ProcessIO,
) (int, error) {
	for _, file := range invocation.ExtraFiles {
		if _, err := file.Stat(); err != nil {
			return 1, err
		}
	}

	e.mu.Lock()
	e.active++
	if e.active > e.maxActive {
		e.maxActive = e.active
	}
	e.modes = append(e.modes, invocation.Mode)
	e.retries = append(e.retries, invocation.allowOverlayReuseRetry)
	e.streams = append(e.streams, streams)
	e.mu.Unlock()

	time.Sleep(e.delay)

	e.mu.Lock()
	e.active--
	code := e.exitCode
	err := e.executeErr
	e.mu.Unlock()

	return code, err
}

func TestRunRetainsExactSourcesSerializesCommandsAndRemovesOverlay(t *testing.T) {
	run, plan, originalSources, executor := testRun(t)

	for _, file := range []*os.File{
		originalSources.OverlayUpper,
		originalSources.OverlayWork,
	} {
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}

	commands := []Command{
		{
			Argv:         []string{"/bin/true"},
			Mode:         ExecutionNonInteractive,
			Root:         true,
			Capabilities: CapabilityRootLifecycle,
		},
		{
			Argv:         []string{"/bin/true"},
			Mode:         ExecutionManagedPTY,
			Capabilities: CapabilityDropAll,
		},
	}
	var wait sync.WaitGroup
	errs := make(chan error, len(commands))
	for _, command := range commands {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := run.Execute(t.Context(), command, ProcessIO{})
			errs <- err
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}

	executor.mu.Lock()
	if executor.maxActive != 1 {
		t.Errorf(
			"same-run active commands = %d, want at most 1",
			executor.maxActive,
		)
	}
	if len(executor.modes) != len(commands) {
		t.Errorf("executed modes = %q", executor.modes)
	}
	if len(executor.retries) != 2 ||
		executor.retries[0] ||
		!executor.retries[1] {
		t.Errorf(
			"overlay retry authorities = %v, want [false true]",
			executor.retries,
		)
	}
	executor.mu.Unlock()

	runRoot := filepath.Dir(plan.Overlay.Upper)
	if err := run.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(runRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("closed run overlay remains: %v", err)
	}
}

func TestRunRejectsOverlayDescriptorsFromAnotherRun(t *testing.T) {
	first, _, sources, _ := testRun(t)
	defer first.Close()

	path := secureRunStorageTestPath(t)
	store, err := OpenRunStorage(path, RunStorageLimits{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	directories, err := store.Create(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer directories.Close()

	plan := first.Plan()
	plan.RunID = directories.ID()
	plan.Overlay = directories.Overlay()
	executor := &recordingProcessExecutor{}
	if run, err := NewRun(plan, sources, directories, executor, nil); err == nil {
		run.Close()
		t.Fatal("mismatched overlay source descriptors were accepted")
	}
}

func TestIndependentRunsOverlapWithSameHomeAndDifferentMountPlans(
	t *testing.T,
) {
	path := secureRunStorageTestPath(t)
	store, err := OpenRunStorage(path, RunStorageLimits{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	firstDirectories, err := store.Create(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	secondDirectories, err := store.Create(t.Context())
	if err != nil {
		firstDirectories.Close()
		t.Fatal(err)
	}

	firstPlan := validPlan()
	firstPlan.RunID = firstDirectories.ID()
	firstPlan.Overlay = firstDirectories.Overlay()
	secondPlan := validPlan()
	secondPlan.RunID = secondDirectories.ID()
	secondPlan.Overlay = secondDirectories.Overlay()
	secondPlan.Projects = []Project{{
		Name:     "other",
		HostPath: "/projects/other",
		Target:   "/toby/workspace/other",
	}}
	secondPlan.ManagedDirectories = []mount.Entry{{
		Key: mount.Key{
			Type:    mount.TypeTool,
			Name:    "codex",
			Purpose: "state",
		},
		Profile:  "default",
		HostPath: "/data/toby/volumes/codex/_data",
		Target:   "/toby/home/.codex",
		Access:   mount.AccessRegular,
	}}
	secondPlan.GeneratedFiles = []GeneratedFile{{
		HostPath: secondPlan.ManagedDirectories[0].HostPath + "/config.toml",
		Target:   "/toby/home/.codex/config.toml",
		Data:     []byte("codex"),
		Mode:     0o600,
		UID:      secondPlan.Identity.HostUID,
		GID:      secondPlan.Identity.HostGID,
	}}
	secondPlan.Workdir = "/toby/workspace/other"

	firstSources := rendererSources(t, firstPlan)
	runStorageRoot, err := store.RootFile()
	if err != nil {
		t.Fatal(err)
	}
	defer runStorageRoot.Close()
	firstSources.ProtectedRoots.RunStorage = runStorageRoot
	firstSources.OverlayUpper, err = firstDirectories.UpperFile()
	if err != nil {
		t.Fatal(err)
	}
	firstSources.OverlayWork, err = firstDirectories.WorkFile()
	if err != nil {
		t.Fatal(err)
	}
	secondSources := rendererSources(t, secondPlan)
	secondSources.ProtectedRoots.ImageStore =
		firstSources.ProtectedRoots.ImageStore
	secondSources.ProtectedRoots.PersistentData =
		firstSources.ProtectedRoots.PersistentData
	secondSources.ProtectedRoots.RunStorage = runStorageRoot
	secondSources.RootFS = firstSources.RootFS
	secondSources.Home = firstSources.Home
	secondManagedPath := filepath.Join(
		firstSources.ProtectedRoots.PersistentData.Name(),
		"volumes",
		"tool-codex",
		"_data",
	)
	if err := os.MkdirAll(secondManagedPath, 0o700); err != nil {
		t.Fatal(err)
	}
	secondManagedKey := secondPlan.ManagedDirectories[0].Key
	secondSources.ManagedDirectories[secondManagedKey] =
		openDirectorySourceAt(t, secondManagedPath)
	secondSources.OverlayUpper, err = secondDirectories.UpperFile()
	if err != nil {
		t.Fatal(err)
	}
	secondSources.OverlayWork, err = secondDirectories.WorkFile()
	if err != nil {
		t.Fatal(err)
	}
	defer firstSources.OverlayUpper.Close()
	defer firstSources.OverlayWork.Close()
	defer secondSources.OverlayUpper.Close()
	defer secondSources.OverlayWork.Close()

	gate := &overlapGate{
		entered: make(chan string, 2),
		release: make(chan struct{}),
	}
	first, err := NewRun(
		firstPlan,
		firstSources,
		firstDirectories,
		&gatedProcessExecutor{name: "opencode", gate: gate},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := NewRun(
		secondPlan,
		secondSources,
		secondDirectories,
		&gatedProcessExecutor{name: "codex", gate: gate},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	command := Command{
		Argv:         []string{"/bin/true"},
		Mode:         ExecutionNonInteractive,
		Capabilities: CapabilityDropAll,
	}
	errorsByRun := make(chan error, 2)
	go func() {
		_, err := first.Execute(t.Context(), command, ProcessIO{})
		errorsByRun <- err
	}()
	go func() {
		_, err := second.Execute(t.Context(), command, ProcessIO{})
		errorsByRun <- err
	}()

	entered := map[string]bool{}
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for len(entered) != 2 {
		select {
		case name := <-gate.entered:
			entered[name] = true
		case <-timer.C:
			t.Fatalf(
				"same-home runs did not overlap; entered = %#v",
				entered,
			)
		}
	}
	close(gate.release)
	for range 2 {
		if err := <-errorsByRun; err != nil {
			t.Fatal(err)
		}
	}

	if first.Plan().Overlay.Upper == second.Plan().Overlay.Upper {
		t.Fatal("independent runs share an overlay upper")
	}
	if first.Plan().Projects[0].Name == second.Plan().Projects[0].Name {
		t.Fatal("independent runs unexpectedly share a project plan")
	}
	if first.Plan().ManagedDirectories[0].Key ==
		second.Plan().ManagedDirectories[0].Key {
		t.Fatal("independent runs unexpectedly share a managed mount plan")
	}

	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if code, err := second.Execute(
		t.Context(),
		command,
		ProcessIO{},
	); err != nil || code != 0 {
		t.Fatalf(
			"second run after first close: code=%d error=%v",
			code,
			err,
		)
	}
}

func testRun(
	t *testing.T,
) (
	*Run,
	Plan,
	Sources,
	*recordingProcessExecutor,
) {
	t.Helper()

	path := secureRunStorageTestPath(t)
	store, err := OpenRunStorage(path, RunStorageLimits{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	directories, err := store.Create(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	plan := validPlan()
	plan.RunID = directories.ID()
	plan.Overlay = directories.Overlay()
	sources := rendererSources(t, plan)
	sources.ProtectedRoots.RunStorage, err = store.RootFile()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sources.ProtectedRoots.RunStorage.Close(); err != nil &&
			!errors.Is(err, os.ErrInvalid) &&
			!errors.Is(err, os.ErrClosed) {
			t.Error(err)
		}
	})
	sources.OverlayUpper, err = directories.UpperFile()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sources.OverlayUpper.Close(); err != nil &&
			!errors.Is(err, os.ErrInvalid) &&
			!errors.Is(err, os.ErrClosed) {
			t.Error(err)
		}
	})
	sources.OverlayWork, err = directories.WorkFile()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sources.OverlayWork.Close(); err != nil &&
			!errors.Is(err, os.ErrInvalid) &&
			!errors.Is(err, os.ErrClosed) {
			t.Error(err)
		}
	})

	executor := &recordingProcessExecutor{delay: 20 * time.Millisecond}
	run, err := NewRun(plan, sources, directories, executor, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := run.Close(); err != nil {
			t.Error(err)
		}
	})

	return run, plan, sources, executor
}
