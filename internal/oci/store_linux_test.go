//go:build linux

package oci

// Exercises immutable publication, progress, pull policies, cleanup, and
// concurrent cache sharing independently of external registries.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	digest "github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/oci/image"
)

type fakePipeline struct {
	mu               sync.Mutex
	pulls            int
	extractions      int
	failPull         bool
	failExtract      bool
	platformAgnostic bool
}

func (p *fakePipeline) pull(
	ctx context.Context,
	_ normalizedRequest,
	layoutPath string,
	reporter ProgressReporter,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	p.mu.Lock()
	p.pulls++
	fail := p.failPull
	platformAgnostic := p.platformAgnostic
	p.mu.Unlock()
	if fail {
		return errors.New("injected registry failure")
	}
	if err := reportProgress(reporter, Progress{
		Phase: ProgressResolving,
	}); err != nil {
		return err
	}
	if err := reportProgress(reporter, Progress{
		Phase:          ProgressDownloading,
		CompletedBytes: 5,
		TotalBytes:     10,
		TotalItems:     1,
	}); err != nil {
		return err
	}
	platform := testPlatform()
	if platformAgnostic {
		platform = ocispec.Platform{}
	}
	if err := writeTestLayout(layoutPath, platform); err != nil {
		return err
	}
	return reportProgress(reporter, Progress{
		Phase:          ProgressDownloading,
		CompletedBytes: 10,
		TotalBytes:     10,
		CompletedItems: 1,
		TotalItems:     1,
	})
}

func (p *fakePipeline) extract(
	ctx context.Context,
	_ string,
	bundlePath string,
	_ ocispec.Manifest,
	reporter ProgressReporter,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	p.mu.Lock()
	p.extractions++
	fail := p.failExtract
	p.mu.Unlock()
	if fail {
		return errors.New("injected extraction failure")
	}
	if err := reportProgress(reporter, Progress{
		Phase:      ProgressExtracting,
		TotalBytes: 10,
		TotalItems: 1,
	}); err != nil {
		return err
	}

	rootfs := filepath.Join(bundlePath, "rootfs")
	if err := os.MkdirAll(rootfs, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(
		filepath.Join(rootfs, "marker"),
		[]byte("prepared\n"),
		0o644,
	); err != nil {
		return err
	}
	return reportProgress(reporter, Progress{
		Phase:          ProgressExtracting,
		CompletedBytes: 10,
		TotalBytes:     10,
		CompletedItems: 1,
		TotalItems:     1,
	})
}

func (p *fakePipeline) counts() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pulls, p.extractions
}

func TestServicePullsExtractsPublishesAndReportsProgress(t *testing.T) {
	t.Parallel()

	pipeline := &fakePipeline{}
	service := newTestStore(t, pipeline)

	var progress []Progress
	prepared, err := service.Prepare(t.Context(), Request{
		Reference:  "example:latest",
		Platform:   testPlatform(),
		PullPolicy: image.PullIfMissing,
		Progress: func(snapshot Progress) error {
			progress = append(progress, snapshot)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()

	pulls, extractions := pipeline.counts()
	if pulls != 1 || extractions != 1 {
		t.Fatalf(
			"pipeline calls = (%d, %d), want (1, 1)",
			pulls,
			extractions,
		)
	}
	if len(progress) != 5 ||
		progress[0].Phase != ProgressResolving ||
		progress[len(progress)-1].Phase != ProgressExtracting ||
		progress[len(progress)-1].CompletedBytes != 10 {
		t.Fatalf("progress = %#v", progress)
	}

	metadata := prepared.Metadata()
	if metadata.Reference != "docker.io/library/example:latest" ||
		metadata.Repository != "docker.io/library/example" ||
		metadata.Manifest.Digest == "" ||
		metadata.Config.Digest == "" ||
		metadata.Manifest.Annotations != nil ||
		metadata.Config.Data != nil ||
		metadata.Runtime.Workdir != "/workspace" {
		t.Fatalf("metadata = %#v", metadata)
	}
	if data, err := os.ReadFile(
		filepath.Join(prepared.RootfsPath(), "marker"),
	); err != nil || string(data) != "prepared\n" {
		t.Fatalf("rootfs marker = %q, %v", data, err)
	}
}

func TestServiceIfMissingAndNeverReusePublishedObject(t *testing.T) {
	t.Parallel()

	paths := testPaths(t)
	firstPipeline := &fakePipeline{}
	first := openTestService(t, paths, firstPipeline)
	prepared, err := first.Prepare(
		t.Context(),
		testRequest(image.PullIfMissing),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	secondPipeline := &fakePipeline{failPull: true}
	second := openTestService(t, paths, secondPipeline)
	for _, policy := range []image.PullPolicy{
		image.PullIfMissing,
		image.PullNever,
	} {
		prepared, err := second.Prepare(t.Context(), testRequest(policy))
		if err != nil {
			t.Fatalf("%s: %v", policy, err)
		}
		if err := prepared.Close(); err != nil {
			t.Fatal(err)
		}
	}
	pulls, extractions := secondPipeline.counts()
	if pulls != 0 || extractions != 0 {
		t.Fatalf(
			"cache reuse pipeline calls = (%d, %d)",
			pulls,
			extractions,
		)
	}
}

func TestServiceAlwaysPullsButReusesExactRootfs(t *testing.T) {
	t.Parallel()

	pipeline := &fakePipeline{}
	service := newTestStore(t, pipeline)
	for _, policy := range []image.PullPolicy{
		image.PullIfMissing,
		image.PullAlways,
	} {
		prepared, err := service.Prepare(t.Context(), testRequest(policy))
		if err != nil {
			t.Fatal(err)
		}
		if err := prepared.Close(); err != nil {
			t.Fatal(err)
		}
	}

	pulls, extractions := pipeline.counts()
	if pulls != 2 || extractions != 1 {
		t.Fatalf(
			"pipeline calls = (%d, %d), want (2, 1)",
			pulls,
			extractions,
		)
	}
}

func TestServiceNeverMissingDoesNotInvokePipeline(t *testing.T) {
	t.Parallel()

	pipeline := &fakePipeline{}
	service := newTestStore(t, pipeline)
	_, err := service.Prepare(t.Context(), testRequest(image.PullNever))
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("Prepare error = %v", err)
	}
	pulls, extractions := pipeline.counts()
	if pulls != 0 || extractions != 0 {
		t.Fatalf(
			"never policy pipeline calls = (%d, %d)",
			pulls,
			extractions,
		)
	}
}

func TestServiceFailedExtractionPublishesNothing(t *testing.T) {
	t.Parallel()

	pipeline := &fakePipeline{failExtract: true}
	service := newTestStore(t, pipeline)
	_, err := service.Prepare(
		t.Context(),
		testRequest(image.PullIfMissing),
	)
	if err == nil || !strings.Contains(err.Error(), "extract OCI image") {
		t.Fatalf("Prepare error = %v", err)
	}

	for _, directory := range []string{"objects", "references", "tmp"} {
		entries, readErr := os.ReadDir(
			filepath.Join(service.root.Path(), directory),
		)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if len(entries) != 0 {
			t.Fatalf("%s contains %v after failure", directory, entries)
		}
	}
}

func TestServiceConcurrentIfMissingPullsOnce(t *testing.T) {
	t.Parallel()

	pipeline := &fakePipeline{}
	service := newTestStore(t, pipeline)

	const callers = 8
	var wait sync.WaitGroup
	errorsFound := make(chan error, callers)
	wait.Add(callers)
	for range callers {
		go func() {
			defer wait.Done()

			prepared, err := service.Prepare(
				context.Background(),
				testRequest(image.PullIfMissing),
			)
			if err == nil {
				err = prepared.Close()
			}
			errorsFound <- err
		}()
	}
	wait.Wait()
	close(errorsFound)

	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
	pulls, extractions := pipeline.counts()
	if pulls != 1 || extractions != 1 {
		t.Fatalf(
			"pipeline calls = (%d, %d), want (1, 1)",
			pulls,
			extractions,
		)
	}
}

func newTestStore(
	t *testing.T,
	pipeline *fakePipeline,
) *Store {
	t.Helper()
	return openTestService(t, testPaths(t), pipeline)
}

func openTestService(
	t *testing.T,
	paths config.Paths,
	pipeline *fakePipeline,
) *Store {
	t.Helper()

	service, err := newStore(
		paths,
		nil,
		options{
			pull:    pipeline.pull,
			extract: pipeline.extract,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if service.root != nil {
			if err := service.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	return service
}

func testPaths(t *testing.T) config.Paths {
	t.Helper()

	root := t.TempDir()
	return config.Paths{
		Home:         root,
		XDGDataHome:  filepath.Join(root, "data"),
		XDGCacheHome: filepath.Join(root, "cache"),
	}
}

func testPlatform() ocispec.Platform {
	return ocispec.Platform{
		OS:           "linux",
		Architecture: "amd64",
	}
}

func testRequest(policy image.PullPolicy) Request {
	return Request{
		Reference:  "example:latest",
		Platform:   testPlatform(),
		PullPolicy: policy,
	}
}

func writeTestLayout(
	path string,
	platform ocispec.Platform,
) error {
	configDocument := ocispec.Image{
		Platform: platform,
		Config: ocispec.ImageConfig{
			Env:        []string{"PATH=/usr/bin:/bin"},
			WorkingDir: "/workspace",
			Entrypoint: []string{},
			Labels:     map[string]string{"test": "true"},
		},
	}
	configData, err := json.Marshal(configDocument)
	if err != nil {
		return err
	}
	configDigest := digest.FromBytes(configData)
	configDescriptor := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageConfig,
		Digest:    configDigest,
		Size:      int64(len(configData)),
		Data:      append([]byte(nil), configData...),
	}

	manifestDocument := ocispec.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    configDescriptor,
	}
	manifestData, err := json.Marshal(manifestDocument)
	if err != nil {
		return err
	}
	manifestDigest := digest.FromBytes(manifestData)
	manifestDescriptor := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageManifest,
		Digest:    manifestDigest,
		Size:      int64(len(manifestData)),
		Annotations: map[string]string{
			ocispec.AnnotationRefName: layoutReferenceName,
		},
	}

	indexDocument := ocispec.Index{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageIndex,
		Manifests: []ocispec.Descriptor{manifestDescriptor},
	}
	indexData, err := json.Marshal(indexDocument)
	if err != nil {
		return err
	}

	for _, blob := range []struct {
		digest digest.Digest
		data   []byte
	}{
		{digest: configDigest, data: configData},
		{digest: manifestDigest, data: manifestData},
	} {
		directory := filepath.Join(
			path,
			"blobs",
			blob.digest.Algorithm().String(),
		)
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(
			filepath.Join(directory, blob.digest.Encoded()),
			blob.data,
			0o644,
		); err != nil {
			return err
		}
	}

	if err := os.WriteFile(
		filepath.Join(path, "index.json"),
		indexData,
		0o644,
	); err != nil {
		return err
	}
	return os.WriteFile(
		filepath.Join(path, "oci-layout"),
		[]byte(`{"imageLayoutVersion":"1.0.0"}`),
		0o644,
	)
}
