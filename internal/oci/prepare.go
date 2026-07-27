package oci

// Selects cached OCI objects or coordinates registry copies, rootless
// extraction, and immutable publication for one image-preparation request.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"petris.dev/toby/internal/oci/image"
	"petris.dev/toby/internal/oci/imagesource"
)

// Prepare returns a descriptor lease for the exact cached or newly published
// rootfs selected by request.
func (s *Store) Prepare(
	ctx context.Context,
	request Request,
) (prepared *Prepared, returnErr error) {
	if ctx == nil {
		return nil, fmt.Errorf("prepare OCI image: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.startOperation(); err != nil {
		return nil, err
	}
	defer s.finishOperation()

	normalized, err := normalizeRequest(request)
	if err != nil {
		return nil, err
	}
	if request.Source == "" {
		request.Source = imagesource.Registry
	}
	if request.PullPolicy == "" {
		request.PullPolicy = image.PullIfMissing
	}
	switch request.PullPolicy {
	case image.PullIfMissing, image.PullAlways, image.PullNever:
	default:
		return nil, fmt.Errorf(
			"invalid OCI pull policy %q",
			request.PullPolicy,
		)
	}
	switch request.Source {
	case imagesource.Registry:
	case imagesource.Archive:
		if request.Archive == "" {
			return nil, fmt.Errorf("OCI archive path must not be empty")
		}
	case imagesource.Build:
		if request.Build.Context == "" ||
			request.Build.Dockerfile == "" {
			return nil, fmt.Errorf(
				"OCI build context and Dockerfile must not be empty",
			)
		}
	default:
		return nil, fmt.Errorf(
			"invalid OCI image source %q",
			request.Source,
		)
	}

	referenceLock, err := s.lockContext(
		ctx,
		filepath.Join("locks", "references", normalized.key+".lock"),
	)
	if err != nil {
		return nil, fmt.Errorf(
			"lock OCI reference %q: %w",
			normalized.reference,
			err,
		)
	}
	defer func() {
		s.logger.DebugError(
			"release OCI reference lock",
			referenceLock.Close(),
			"reference",
			normalized.reference,
		)
	}()

	if request.PullPolicy != image.PullAlways {
		cached, found, err := s.openCached(ctx, normalized)
		if err != nil {
			return nil, err
		}
		if found {
			return cached, nil
		}
	}
	if request.PullPolicy == image.PullNever {
		return nil, fmt.Errorf(
			"OCI image %q is not available in the per-user cache",
			normalized.reference,
		)
	}

	return s.materializeAndPrepare(ctx, normalized, request)
}

func (s *Store) materializeAndPrepare(
	ctx context.Context,
	request normalizedRequest,
	input Request,
) (prepared *Prepared, returnErr error) {
	temporary, err := os.MkdirTemp(
		filepath.Join(s.root.Path(), "tmp"),
		"prepare-",
	)
	if err != nil {
		return nil, fmt.Errorf("create temporary OCI object: %w", err)
	}
	defer func() {
		if temporary != "" {
			s.logger.DebugError(
				"remove temporary OCI object",
				removeTemporaryObject(temporary),
				"path",
				temporary,
			)
		}
	}()

	layoutPath := filepath.Join(temporary, "layout")
	switch input.Source {
	case imagesource.Registry:
		err = s.pull(ctx, request, layoutPath, input.Progress)
	case imagesource.Archive:
		err = extractOCIArchive(ctx, input.Archive, layoutPath)
	case imagesource.Build:
		err = s.materializeBuild(
			ctx,
			input.Build,
			request.platform,
			layoutPath,
			input.Stdout,
			input.Stderr,
		)
	default:
		err = fmt.Errorf("unsupported OCI source %q", input.Source)
	}
	if err != nil {
		return nil, fmt.Errorf(
			"materialize OCI image %q: %w",
			request.reference,
			err,
		)
	}

	layoutImage, err := s.readLayoutImage(layoutPath, request.platform)
	if err != nil {
		return nil, fmt.Errorf(
			"read OCI layout for %q: %w",
			request.reference,
			err,
		)
	}
	metadata := Metadata{
		Reference:  request.reference,
		Repository: request.repository,
		Spec:       layoutImage.spec,
	}

	objectKey, err := immutableObjectKey(layoutImage.spec)
	if err != nil {
		return nil, err
	}
	objectLock, err := s.lockContext(ctx, objectLockName(objectKey))
	if err != nil {
		return nil, fmt.Errorf(
			"lock OCI object %s: %w",
			layoutImage.spec.Manifest.Digest,
			err,
		)
	}
	defer func() {
		if objectLock != nil {
			s.logger.DebugError(
				"release OCI object lock",
				objectLock.Close(),
				"object",
				objectKey,
			)
		}
	}()

	objectPath := s.objectPath(objectKey)
	existing, found, err := s.openObject(objectPath, metadata)
	if err != nil {
		return nil, err
	}
	if found {
		if err := s.publishReference(request, objectKey); err != nil {
			s.logger.DebugError(
				"close cached OCI object after reference publication failure",
				existing.Close(),
			)
			return nil, err
		}
		s.logger.DebugError("close cached OCI object", existing.Close())
		retainedLock := objectLock
		objectLock = nil
		s.logger.DebugError("release OCI object lock", retainedLock.Close())
		return s.retainPreparedObject(ctx, objectKey, metadata)
	}

	if err := s.extract(
		ctx,
		layoutPath,
		filepath.Join(temporary, "bundle"),
		layoutImage.manifest,
		input.Progress,
	); err != nil {
		return nil, fmt.Errorf(
			"extract OCI image %q: %w",
			request.reference,
			err,
		)
	}

	if err := s.writeObjectMetadata(temporary, layoutImage.spec); err != nil {
		return nil, err
	}
	if err := s.publishObject(temporary, objectKey); err != nil {
		return nil, err
	}
	temporary = ""

	prepared, found, err = s.openObject(objectPath, metadata)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf(
			"published OCI object %s is missing",
			layoutImage.spec.Manifest.Digest,
		)
	}
	if err := s.publishReference(request, objectKey); err != nil {
		s.logger.DebugError(
			"close prepared OCI object after reference publication failure",
			prepared.Close(),
		)
		return nil, err
	}
	s.logger.DebugError("close prepared OCI object", prepared.Close())
	retainedLock := objectLock
	objectLock = nil
	s.logger.DebugError("release OCI object lock", retainedLock.Close())

	return s.retainPreparedObject(ctx, objectKey, metadata)
}

func (s *Store) openCached(
	ctx context.Context,
	request normalizedRequest,
) (*Prepared, bool, error) {
	record, found, err := s.readReference(request)
	if err != nil || !found {
		return nil, false, err
	}

	metadata := Metadata{
		Reference:  request.reference,
		Repository: request.repository,
	}
	prepared, found, err := s.retainObject(
		ctx,
		record.Object,
		metadata,
	)
	if err != nil || !found {
		return prepared, found, err
	}
	key, err := immutableObjectKey(prepared.Spec())
	if err != nil || key != record.Object {
		s.logger.DebugError(
			"close mismatched cached OCI object",
			prepared.Close(),
			"reference", request.reference,
		)
		if err != nil {
			return nil, false, fmt.Errorf(
				"identify cached OCI object for reference %q: %w",
				request.reference,
				err,
			)
		}
		return nil, false, fmt.Errorf(
			"cached OCI reference %q points to the wrong object",
			request.reference,
		)
	}

	return prepared, true, nil
}

func (s *Store) retainPreparedObject(
	ctx context.Context,
	object string,
	metadata Metadata,
) (*Prepared, error) {
	prepared, found, err := s.retainObject(ctx, object, metadata)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf(
			"published OCI object %s is missing",
			metadata.Manifest.Digest,
		)
	}
	return prepared, nil
}
