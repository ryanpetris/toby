//go:build linux

package caddy

// Starts one descriptor-authoritative, read-only Caddy Bubblewrap generation.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/agent/resource"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/oci"
	"petris.dev/toby/internal/sandbox/bwrap"
	"petris.dev/toby/internal/sandbox/mount"
)

type factory struct {
	image          *oci.Prepared
	storage        *bwrap.RunStorage
	executor       *bwrap.Executor
	authPath       string
	auth           *os.File
	resolver       *os.File
	readinessLimit time.Duration
	readinessPoll  time.Duration
	uid            int
	gid            int
	logger         *diagnostic.Logger
}

var _ resource.Factory = (*factory)(nil)

func (f *factory) Start(
	ctx context.Context,
	_ resource.Key,
	generation uint64,
) (result resource.Instance, returnErr error) {
	if f == nil ||
		f.image == nil ||
		f.storage == nil ||
		f.executor == nil ||
		f.auth == nil ||
		f.resolver == nil {
		return nil, fmt.Errorf(
			"caddy generation factory is not configured",
		)
	}
	progress := progressFrom(ctx)
	assembleOperation := startProgress(
		progress,
		"Assembling Caddy sandbox",
	)
	defer func() {
		if returnErr != nil {
			assembleOperation.Fail("Caddy sandbox assembly failed")
		}
	}()

	directories, err := f.storage.Create(ctx)
	if err != nil {
		return nil, err
	}
	cleanupDirectories := true
	defer func() {
		if cleanupDirectories {
			f.logger.DebugError(
				"close failed Caddy run directories",
				directories.Close(),
			)
		}
	}()

	rootfs, err := f.image.RootfsFile()
	if err != nil {
		return nil, fmt.Errorf("open Caddy rootfs capability")
	}
	defer func() {
		f.logger.DebugError(
			"close Caddy rootfs capability",
			rootfs.Close(),
		)
	}()
	runtimeDirectory, err := directories.RuntimeFile()
	if err != nil {
		return nil, fmt.Errorf("open Caddy runtime capability")
	}
	defer func() {
		f.logger.DebugError(
			"close Caddy runtime capability",
			runtimeDirectory.Close(),
		)
	}()
	auth, err := f.duplicateFile(f.auth)
	if err != nil {
		return nil, fmt.Errorf(
			"duplicate Caddy authorization capability",
		)
	}
	defer func() {
		f.logger.DebugError(
			"close Caddy authorization capability",
			auth.Close(),
		)
	}()
	resolver, err := f.duplicateFile(f.resolver)
	if err != nil {
		return nil, fmt.Errorf(
			"duplicate Caddy resolver capability",
		)
	}
	defer func() {
		f.logger.DebugError(
			"close Caddy resolver capability",
			resolver.Close(),
		)
	}()

	plan := bwrap.BackgroundServicePlan{
		ID: defaultServiceIDPrefix +
			fmt.Sprintf("%d", generation),
		RootFS: bwrap.RootFS{
			Path:   f.image.RootfsPath(),
			Digest: f.image.Spec().Config.Digest.String(),
		},
		Binds: []mount.Bind{
			{
				HostPath: f.authPath,
				Target:   defaultAuthSocket,
				Access:   mount.AccessReadOnly,
			},
			{
				HostPath: defaultResolverSource,
				Target:   defaultResolverTarget,
				Access:   mount.AccessReadOnly,
			},
		},
		Runtime: &bwrap.RuntimeAsset{
			HostPath: directories.RuntimePath(),
			Target:   bwrap.BackgroundServiceRuntimeTarget,
			Access:   mount.AccessRegular,
		},
		Workdir: defaultServiceWorkdir,
		Environment: []bwrap.EnvironmentVariable{
			{Name: "XDG_CONFIG_HOME", Value: "/tmp/config"},
			{Name: "XDG_DATA_HOME", Value: "/tmp/data"},
		},
		Identity: bwrap.Identity{
			HostUID: f.uid,
			HostGID: f.gid,
		},
		Network: bwrap.NetworkHost,
		Command: append([]string(nil), defaultCommand...),
	}
	invocation, err := bwrap.RenderBackgroundService(
		plan,
		bwrap.BackgroundServiceSources{
			RootFS: rootfs,
			Binds: map[string]*os.File{
				defaultAuthSocket:     auth,
				defaultResolverTarget: resolver,
			},
			Runtime: runtimeDirectory,
		},
	)
	if err != nil {
		return nil, err
	}
	assembleOperation.Complete("Caddy sandbox assembled")

	bootstrap, err := bootstrapFile(f.logger)
	if err != nil {
		f.logger.DebugError(
			"close unlaunched Caddy invocation",
			invocation.Close(),
		)
		return nil, err
	}
	defer func() {
		f.logger.DebugError(
			"close Caddy bootstrap input",
			bootstrap.Close(),
		)
	}()
	nullOutput, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		f.logger.DebugError(
			"close unlaunched Caddy invocation",
			invocation.Close(),
		)
		return nil, err
	}
	defer func() {
		f.logger.DebugError(
			"close Caddy output sink",
			nullOutput.Close(),
		)
	}()
	nullError, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		f.logger.DebugError(
			"close unlaunched Caddy invocation",
			invocation.Close(),
		)
		return nil, err
	}
	defer func() {
		f.logger.DebugError(
			"close Caddy error sink",
			nullError.Close(),
		)
	}()

	launchOperation := startProgress(
		progress,
		"Launching Caddy",
	)
	background, err := f.executor.StartBackground(
		ctx,
		invocation,
		bwrap.ProcessIO{
			Stdin:  bootstrap,
			Stdout: nullOutput,
			Stderr: nullError,
		},
	)
	if err != nil {
		launchOperation.Fail("Caddy launch failed")
		return nil, err
	}
	launchOperation.Complete("Caddy launched")

	adminPath := filepath.Join(
		directories.RuntimePath(),
		"admin.sock",
	)
	dataPath := filepath.Join(
		directories.RuntimePath(),
		"data.sock",
	)
	instance, err := newInstance(
		background,
		directories,
		adminPath,
		dataPath,
		f.uid,
		f.gid,
		f.logger,
	)
	if err != nil {
		killCtx, cancel := context.WithTimeout(
			context.Background(),
			time.Second,
		)
		defer cancel()
		f.logger.DebugError(
			"kill Caddy after instance setup failed",
			background.Kill(killCtx),
		)
		return nil, err
	}
	cleanupDirectories = false

	readyCtx, cancel := context.WithTimeout(
		ctx,
		f.readinessLimit,
	)
	defer cancel()
	readyOperation := startProgress(
		progress,
		"Waiting for Caddy readiness",
	)
	if err := instance.waitReady(readyCtx, f.readinessPoll); err != nil {
		readyOperation.Fail("Caddy readiness check failed")
		return instance, err
	}
	readyOperation.Complete("Caddy is accepting configuration")

	return instance, nil
}

func (f *factory) duplicateFile(source *os.File) (*os.File, error) {
	if source == nil {
		return nil, os.ErrInvalid
	}

	descriptor, err := unix.FcntlInt(
		source.Fd(),
		unix.F_DUPFD_CLOEXEC,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(
		uintptr(descriptor),
		source.Name()+" duplicate",
	)
	if file == nil {
		f.logger.DebugError(
			"close invalid duplicated Caddy descriptor",
			unix.Close(descriptor),
		)
		return nil, os.ErrInvalid
	}

	return file, nil
}
