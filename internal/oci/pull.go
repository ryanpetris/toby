package oci

// Copies one selected registry image into a verified local OCI layout.

import (
	"context"
	"fmt"
	"io"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func pullImage(
	ctx context.Context,
	request normalizedRequest,
	layoutPath string,
	reporter ProgressReporter,
) error {
	if err := reportProgress(reporter, Progress{
		Phase: ProgressResolving,
	}); err != nil {
		return err
	}

	reference, err := name.ParseReference(
		request.reference,
		name.StrictValidation,
	)
	if err != nil {
		return fmt.Errorf(
			"parse registry reference %q: %w",
			request.reference,
			err,
		)
	}

	image, err := remote.Image(
		reference,
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(authn.DefaultKeychain),
		remote.WithPlatform(v1.Platform{
			OS:           request.platform.OS,
			Architecture: request.platform.Architecture,
			Variant:      request.platform.Variant,
			OSVersion:    request.platform.OSVersion,
			OSFeatures:   request.platform.OSFeatures,
		}),
	)
	if err != nil {
		return fmt.Errorf("resolve registry image: %w", err)
	}

	manifest, err := image.Manifest()
	if err != nil {
		return fmt.Errorf("read registry image manifest: %w", err)
	}
	sizes := make([]int64, 0, len(manifest.Layers)+1)
	for _, descriptor := range manifest.Layers {
		sizes = append(sizes, descriptor.Size)
	}
	configIndex := len(sizes)
	sizes = append(sizes, manifest.Config.Size)

	progress, err := newTransferProgress(
		ProgressDownloading,
		sizes,
		reporter,
	)
	if err != nil {
		return err
	}
	tracked := &progressImage{
		Image:       image,
		progress:    progress,
		configIndex: configIndex,
	}

	destination, err := layout.Write(layoutPath, empty.Index)
	if err != nil {
		return fmt.Errorf("create OCI layout: %w", err)
	}
	if err := destination.AppendImage(
		tracked,
		layout.WithAnnotations(map[string]string{
			ocispec.AnnotationRefName: layoutReferenceName,
		}),
		layout.WithPlatform(v1.Platform{
			OS:           request.platform.OS,
			Architecture: request.platform.Architecture,
			Variant:      request.platform.Variant,
			OSVersion:    request.platform.OSVersion,
			OSFeatures:   request.platform.OSFeatures,
		}),
	); err != nil {
		return fmt.Errorf("write OCI layout: %w", err)
	}
	if err := progress.finish(); err != nil {
		return err
	}

	return nil
}

type progressImage struct {
	v1.Image

	progress    *transferProgress
	configIndex int
}

var _ v1.Image = (*progressImage)(nil)

func (i *progressImage) Layers() ([]v1.Layer, error) {
	layers, err := i.Image.Layers()
	if err != nil {
		return nil, err
	}

	result := make([]v1.Layer, len(layers))
	for index, layer := range layers {
		result[index] = &progressLayer{
			Layer:    layer,
			progress: i.progress,
			index:    index,
		}
	}

	return result, nil
}

func (i *progressImage) RawConfigFile() ([]byte, error) {
	data, err := i.Image.RawConfigFile()
	if err != nil {
		return nil, err
	}
	if err := i.progress.complete(i.configIndex); err != nil {
		return nil, err
	}

	return data, nil
}

type progressLayer struct {
	v1.Layer

	progress *transferProgress
	index    int
}

var _ v1.Layer = (*progressLayer)(nil)

func (l *progressLayer) Compressed() (io.ReadCloser, error) {
	reader, err := l.Layer.Compressed()
	if err != nil {
		return nil, err
	}

	return &progressReadCloser{
		ReadCloser: reader,
		progress:   l.progress,
		index:      l.index,
	}, nil
}
