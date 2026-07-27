package sidecar

// Owns one exact image and mount set and starts one sidecar process.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"petris.dev/toby/internal/agent/resource"
	"petris.dev/toby/internal/sandbox/bwrap"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/sandbox/mount"
)

// Prepared owns one exact image and mount set until Start transfers them to a
// running process or Close discards them.
type Prepared struct {
	mu sync.Mutex

	preparer    *Preparer
	image       Image
	definition  Definition
	metadata    Metadata
	binds       []mount.Bind
	bindSources map[string]*os.File
	environment []bwrap.EnvironmentVariable
	closed      bool
	started     bool
}

// Metadata returns a detached safe planning value.
func (p *Prepared) Metadata() Metadata {
	if p == nil {
		return Metadata{}
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return Metadata{}
	}

	return p.metadata
}

// Start launches one fresh sidecar overlay. withRuntime binds the run's
// private runtime directory at /run/toby for a UDS-producing server.
func (p *Prepared) Start(
	ctx context.Context,
	streams bwrap.ProcessIO,
	withRuntime bool,
) (result *Process, returnErr error) {
	if p == nil {
		return nil, fmt.Errorf("prepared sidecar is nil")
	}
	if ctx == nil {
		return nil, fmt.Errorf("start sidecar context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	p.mu.Lock()
	if p.closed || p.image == nil {
		p.mu.Unlock()
		return nil, fmt.Errorf("prepared sidecar is closed")
	}
	if p.started {
		p.mu.Unlock()
		return nil, fmt.Errorf("prepared sidecar has already started")
	}
	p.started = true
	p.mu.Unlock()

	directories, err := p.preparer.storage.Create(ctx)
	if err != nil {
		p.preparer.logger.DebugError(
			"close prepared sidecar after run directory creation failure",
			p.Close(),
		)
		return nil, err
	}
	defer func() {
		if returnErr != nil {
			p.preparer.logger.DebugError(
				"close sidecar run directories after startup failure",
				directories.Close(),
			)
			p.preparer.logger.DebugError(
				"close prepared sidecar after startup failure",
				p.Close(),
			)
		}
	}()

	sources, closeSources, err := p.sources(directories, withRuntime)
	if err != nil {
		return nil, err
	}

	plan, err := p.plan(directories, withRuntime)
	if err != nil {
		p.preparer.logger.DebugError(
			"close sidecar launch sources",
			closeSources(),
		)
		return nil, err
	}
	invocation, err := bwrap.RenderSidecar(plan, sources)
	if err != nil {
		p.preparer.logger.DebugError(
			"close sidecar launch sources",
			closeSources(),
		)
		return nil, err
	}

	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		p.preparer.logger.DebugError(
			"close unlaunched sidecar invocation",
			invocation.Close(),
		)
		p.preparer.logger.DebugError(
			"close sidecar launch sources",
			closeSources(),
		)
		return nil, fmt.Errorf("create sidecar stderr drain: %w", err)
	}
	logger := p.preparer.logger
	drainDone := make(chan error, 1)
	go func() {
		_, copyErr := io.Copy(io.Discard, stderrReader)
		logger.DebugError(
			"close sidecar stderr reader",
			stderrReader.Close(),
		)
		drainDone <- copyErr
	}()
	streams.Stderr = stderrWriter

	background, err := p.preparer.executor.StartBackground(
		ctx,
		invocation,
		streams,
	)
	writerErr := stderrWriter.Close()
	sourceErr := closeSources()
	if err != nil {
		readerErr := stderrReader.Close()
		drainErr := <-drainDone
		p.preparer.logger.DebugError(
			"close sidecar stderr writer after startup failure",
			writerErr,
		)
		p.preparer.logger.DebugError(
			"close sidecar launch sources after startup failure",
			sourceErr,
		)
		p.preparer.logger.DebugError(
			"close sidecar stderr reader after startup failure",
			readerErr,
		)
		p.preparer.logger.DebugError(
			"drain sidecar stderr after startup failure",
			drainErr,
		)
		return nil, err
	}
	p.preparer.logger.DebugError(
		"close sidecar stderr writer after startup",
		writerErr,
	)
	p.preparer.logger.DebugError(
		"close sidecar launch sources after startup",
		sourceErr,
	)

	runtimePath := ""
	if withRuntime {
		runtimePath = directories.RuntimePath()
	}
	result = newProcess(
		background,
		directories,
		p,
		runtimePath,
		drainDone,
		p.preparer.logger,
	)
	directories = nil
	p = nil

	return result, nil
}

// Close releases an unstarted prepared sidecar.
func (p *Prepared) Close() error {
	if p == nil {
		return nil
	}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	image := p.image
	sources := p.bindSources
	p.image = nil
	p.bindSources = nil
	p.binds = nil
	p.environment = nil
	p.definition = Definition{}
	p.metadata = Metadata{}
	p.mu.Unlock()

	if image != nil {
		p.preparer.logger.DebugError(
			"close prepared sidecar image",
			image.Close(),
		)
	}
	p.preparer.logger.DebugError(
		"close prepared sidecar mount sources",
		closeFiles(sources),
	)
	return nil
}

func (p *Prepared) plan(
	directories *bwrap.RunDirectories,
	withRuntime bool,
) (bwrap.SidecarPlan, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	spec := p.image.Spec()
	network := bwrap.NetworkHost
	if p.definition.Network == resource.NetworkPrivate {
		network = bwrap.NetworkPrivate
	}
	var runtimeAsset *bwrap.RuntimeAsset
	if withRuntime {
		runtimeAsset = &bwrap.RuntimeAsset{
			HostPath: directories.RuntimePath(),
			Target:   layout.Runtime,
			Access:   mount.AccessRegular,
		}
	}

	return bwrap.SidecarPlan{
		ID: directories.ID(),
		RootFS: bwrap.RootFS{
			Digest: spec.Manifest.Digest.String(),
			Path:   p.image.RootfsPath(),
		},
		Overlay: directories.Overlay(),
		Binds:   append([]mount.Bind(nil), p.binds...),
		Runtime: runtimeAsset,
		Workdir: p.metadata.Workdir,
		Environment: append(
			[]bwrap.EnvironmentVariable(nil),
			p.environment...,
		),
		Identity: bwrap.Identity{
			HostUID: os.Geteuid(),
			HostGID: os.Getegid(),
		},
		Network: network,
		Command: append(
			[]string(nil),
			p.definition.Command...,
		),
	}, nil
}

func (p *Prepared) sources(
	directories *bwrap.RunDirectories,
	withRuntime bool,
) (bwrap.SidecarSources, func() error, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	var opened []*os.File
	closeAll := func() error {
		var closeErr error
		for index, file := range opened {
			if file != nil {
				closeErr = errors.Join(closeErr, file.Close())
				opened[index] = nil
			}
		}
		return closeErr
	}

	rootfs, err := p.image.RootfsFile()
	if err != nil {
		return bwrap.SidecarSources{}, closeAll, err
	}
	opened = append(opened, rootfs)
	upper, err := directories.UpperFile()
	if err != nil {
		p.preparer.logger.DebugError(
			"close sidecar sources after upper capability failure",
			closeAll(),
		)
		return bwrap.SidecarSources{}, closeAll, err
	}
	opened = append(opened, upper)
	work, err := directories.WorkFile()
	if err != nil {
		p.preparer.logger.DebugError(
			"close sidecar sources after work capability failure",
			closeAll(),
		)
		return bwrap.SidecarSources{}, closeAll, err
	}
	opened = append(opened, work)

	binds := make(map[string]*os.File, len(p.bindSources))
	for target, source := range p.bindSources {
		duplicate, err := duplicateFile(source, p.preparer.logger)
		if err != nil {
			p.preparer.logger.DebugError(
				"close sidecar sources after bind duplication failure",
				closeAll(),
			)
			return bwrap.SidecarSources{}, closeAll, err
		}
		opened = append(opened, duplicate)
		binds[target] = duplicate
	}

	var runtimeFile *os.File
	if withRuntime {
		runtimeFile, err = directories.RuntimeFile()
		if err != nil {
			p.preparer.logger.DebugError(
				"close sidecar sources after runtime capability failure",
				closeAll(),
			)
			return bwrap.SidecarSources{}, closeAll, err
		}
		opened = append(opened, runtimeFile)
	}

	return bwrap.SidecarSources{
		RootFS:       rootfs,
		OverlayUpper: upper,
		OverlayWork:  work,
		Binds:        binds,
		Runtime:      runtimeFile,
	}, closeAll, nil
}
