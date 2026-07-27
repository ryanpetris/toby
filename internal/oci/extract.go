package oci

// Rootlessly extracts one local OCI image while tracking verified blob reads.

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"

	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	rspec "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/opencontainers/umoci/oci/cas"
	casdir "github.com/opencontainers/umoci/oci/cas/dir"
	"github.com/opencontainers/umoci/oci/layer"

	"petris.dev/toby/internal/diagnostic"
)

func extractImage(
	ctx context.Context,
	layoutPath string,
	bundlePath string,
	manifest ocispec.Manifest,
	reporter ProgressReporter,
	logger *diagnostic.Logger,
) (returnErr error) {
	sizes := make([]int64, len(manifest.Layers))
	for index, descriptor := range manifest.Layers {
		sizes[index] = descriptor.Size
	}
	progress, err := newTransferProgress(
		ProgressExtracting,
		sizes,
		reporter,
	)
	if err != nil {
		return err
	}

	engine, err := casdir.Open(layoutPath)
	if err != nil {
		return fmt.Errorf("open OCI layout for extraction: %w", err)
	}
	defer func() {
		logger.DebugError("close OCI extraction engine", engine.Close())
	}()

	tracked := newExtractionEngine(engine, manifest, progress)
	nextLayer := 0
	options := layer.UnpackOptions{
		OnDiskFormat: layer.DirRootfs{
			MapOptions: rootlessMapOptions(),
		},
		AfterLayerUnpack: func(
			ocispec.Manifest,
			ocispec.Descriptor,
		) error {
			err := progress.complete(nextLayer)
			nextLayer++
			return err
		},
	}
	if err := layer.UnpackManifest(
		ctx,
		tracked,
		bundlePath,
		manifest,
		&options,
	); err != nil {
		return fmt.Errorf("unpack OCI image: %w", err)
	}

	return progress.finish()
}

func rootlessMapOptions() layer.MapOptions {
	return layer.MapOptions{
		UIDMappings: []rspec.LinuxIDMapping{{
			ContainerID: 0,
			HostID:      uint32(os.Geteuid()),
			Size:        1,
		}},
		GIDMappings: []rspec.LinuxIDMapping{{
			ContainerID: 0,
			HostID:      uint32(os.Getegid()),
			Size:        1,
		}},
		Rootless: true,
	}
}

type extractionEngine struct {
	cas.Engine

	mu       sync.Mutex
	progress *transferProgress
	pending  map[digest.Digest][]int
}

var _ cas.Engine = (*extractionEngine)(nil)

func newExtractionEngine(
	engine cas.Engine,
	manifest ocispec.Manifest,
	progress *transferProgress,
) *extractionEngine {
	pending := make(map[digest.Digest][]int, len(manifest.Layers))
	for index, descriptor := range manifest.Layers {
		pending[descriptor.Digest] = append(
			pending[descriptor.Digest],
			index,
		)
	}

	return &extractionEngine{
		Engine:   engine,
		progress: progress,
		pending:  pending,
	}
}

func (e *extractionEngine) GetBlob(
	ctx context.Context,
	digest digest.Digest,
) (io.ReadCloser, error) {
	reader, err := e.Engine.GetBlob(ctx, digest)
	if err != nil {
		return nil, err
	}

	e.mu.Lock()
	indexes := e.pending[digest]
	if len(indexes) != 0 {
		e.pending[digest] = indexes[1:]
	}
	e.mu.Unlock()
	if len(indexes) == 0 {
		return reader, nil
	}

	return &progressReadCloser{
		ReadCloser: reader,
		progress:   e.progress,
		index:      indexes[0],
	}, nil
}
