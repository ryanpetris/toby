package oci

// Coordinates per-user registry copies, rootless unpacking, immutable object
// publication, reference mappings, and rootfs descriptor leases.

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/storage/safefs"
)

const (
	metadataSchemaVersion = 1
	metadataFileMode      = 0o600
	defaultLockRetry      = 25 * time.Millisecond
)

type options struct {
	pull              pullImageFunc
	extract           extractImageFunc
	lockRetryInterval time.Duration
}

func defaultOptions() options {
	return options{
		pull:              pullImage,
		lockRetryInterval: defaultLockRetry,
	}
}

type pullImageFunc func(
	context.Context,
	normalizedRequest,
	string,
	ProgressReporter,
) error

type extractImageFunc func(
	context.Context,
	string,
	string,
	ocispec.Manifest,
	ProgressReporter,
) error

// Store owns one process's image-store capability.
type Store struct {
	mu sync.Mutex

	root              *safefs.Directory
	pull              pullImageFunc
	extract           extractImageFunc
	uid               int
	gid               int
	logger            *diagnostic.Logger
	lockRetryInterval time.Duration
	active            int
	closed            bool
}

var _ io.Closer = (*Store)(nil)

// NewStore opens or creates the current user's private OCI image store.
func NewStore(
	paths config.Paths,
	diagnostics *diagnostic.Service,
) (*Store, error) {
	return newStore(paths, diagnostics, defaultOptions())
}

func newStore(
	paths config.Paths,
	diagnostics *diagnostic.Service,
	options options,
) (*Store, error) {
	if options.pull == nil {
		options.pull = pullImage
	}
	if options.lockRetryInterval <= 0 {
		options.lockRetryInterval = defaultLockRetry
	}

	uid, gid := os.Geteuid(), os.Getegid()
	logger := diagnostics.Logger("oci")
	if options.extract == nil {
		options.extract = func(
			ctx context.Context,
			layoutPath string,
			bundlePath string,
			manifest ocispec.Manifest,
			reporter ProgressReporter,
		) error {
			return extractImage(
				ctx,
				layoutPath,
				bundlePath,
				manifest,
				reporter,
				logger,
			)
		}
	}
	dataRoot, err := safefs.OpenOrCreateRoot(
		paths.TobyDataDir(),
		safefs.DirectoryOptions{
			OwnerUID: uid,
			OwnerGID: gid,
			Logger:   logger,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("open Toby data root for OCI storage: %w", err)
	}
	root, err := dataRoot.MkdirAll("images")
	if err != nil {
		logger.DebugError(
			"close Toby data root after OCI image store setup failed",
			dataRoot.Close(),
		)
		return nil, fmt.Errorf(
			"open per-user OCI image store: %w",
			err,
		)
	}
	root.RepairPrivateOwnershipAndMode()
	logger.DebugError(
		"close Toby data root after opening OCI storage",
		dataRoot.Close(),
	)

	for _, name := range []string{
		"objects",
		"references",
		filepath.Join("locks", "objects"),
		filepath.Join("locks", "references"),
		"tmp",
	} {
		directory, err := root.MkdirAll(name)
		if err != nil {
			logger.DebugError(
				"close OCI image store after directory setup failed",
				root.Close(),
			)
			return nil, fmt.Errorf(
				"prepare OCI store directory %q: %w",
				name,
				err,
			)
		}
		directory.RepairPrivateOwnershipAndMode()
		logger.DebugError(
			"close OCI store directory",
			directory.Close(),
			"directory", name,
		)
	}

	return &Store{
		root:              root,
		pull:              options.pull,
		extract:           options.extract,
		uid:               uid,
		gid:               gid,
		logger:            logger,
		lockRetryInterval: options.lockRetryInterval,
	}, nil
}

// ImageStoreFile returns a caller-owned descriptor for the complete per-user
// OCI store so the sandbox can protect it from writable binds.
func (s *Store) ImageStoreFile() (*os.File, error) {
	if s == nil {
		return nil, fmt.Errorf("OCI store is nil")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.root == nil {
		return nil, fmt.Errorf("OCI store is closed")
	}

	return s.root.File()
}

// Close releases the store-owned image-store capability. Prepared values
// retain independent rootfs descriptors.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	if s.active != 0 {
		active := s.active
		s.mu.Unlock()
		return fmt.Errorf("close OCI store: %d operations are active", active)
	}

	s.closed = true
	root := s.root
	s.root = nil
	s.pull = nil
	s.extract = nil
	s.mu.Unlock()

	s.logger.DebugError(
		"close OCI image store",
		root.Close(),
	)
	return nil
}

func (s *Store) startOperation() error {
	if s == nil {
		return fmt.Errorf("OCI store is nil")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed ||
		s.root == nil ||
		s.pull == nil ||
		s.extract == nil {
		return fmt.Errorf("OCI store is closed")
	}

	s.active++
	return nil
}

func (s *Store) finishOperation() {
	s.mu.Lock()
	s.active--
	s.mu.Unlock()
}
