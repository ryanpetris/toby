//go:build linux

package caddy

// Supervises one Caddy Bubblewrap generation, its protected sockets, and exact
// transient runtime cleanup.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"petris.dev/toby/internal/agent/resource"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/providergateway"
	"petris.dev/toby/internal/sandbox/bwrap"
)

// Instance owns one running Caddy generation and its retained capabilities.
type Instance struct {
	background  bwrap.BackgroundProcess
	directories *bwrap.RunDirectories
	runtime     *os.File
	admin       *Client
	adminPath   string
	dataPath    string
	uid         int
	gid         int
	logger      *diagnostic.Logger

	operations sync.Mutex
	runtimeMu  sync.RWMutex
	done       chan struct{}

	mu  sync.Mutex
	err error
}

var _ resource.Instance = (*Instance)(nil)

func newInstance(
	background bwrap.BackgroundProcess,
	directories *bwrap.RunDirectories,
	adminPath string,
	dataPath string,
	uid, gid int,
	logger *diagnostic.Logger,
) (*Instance, error) {
	if background == nil ||
		directories == nil ||
		adminPath == "" ||
		dataPath == "" ||
		uid < 0 ||
		gid < 0 {
		return nil, fmt.Errorf("caddy process contract is invalid")
	}
	runtime, err := directories.RuntimeFile()
	if err != nil {
		return nil, fmt.Errorf(
			"retain Caddy runtime capability",
		)
	}

	result := &Instance{
		background:  background,
		directories: directories,
		runtime:     runtime,
		adminPath:   adminPath,
		dataPath:    dataPath,
		uid:         uid,
		gid:         gid,
		done:        make(chan struct{}),
		logger:      logger,
	}
	admin, err := New(
		result.connectAdmin,
		Options{Logger: logger},
	)
	if err != nil {
		logger.DebugError(
			"close Caddy runtime capability after client initialization failure",
			runtime.Close(),
		)
		return nil, err
	}
	result.admin = admin
	go result.wait()

	return result, nil
}

// DialData opens the verified data socket through the retained runtime
// directory capability, avoiding the Unix-address length of its diagnostic
// host path.
func (i *Instance) DialData(
	ctx context.Context,
) (*net.UnixConn, error) {
	if i == nil {
		return nil, ErrUnavailable
	}

	select {
	case <-i.done:
		return nil, ErrUnavailable
	default:
	}

	i.runtimeMu.RLock()
	defer i.runtimeMu.RUnlock()
	if i.runtime == nil {
		return nil, ErrUnavailable
	}

	return dialSocket(
		ctx,
		i.runtime,
		filepath.Base(i.dataPath),
	)
}

// Load atomically applies one full configuration and verifies both socket
// identities before the generation may be published.
func (i *Instance) Load(
	ctx context.Context,
	config []byte,
) error {
	if i == nil || i.admin == nil {
		return ErrUnavailable
	}
	if ctx == nil {
		return ErrInvalidRequest
	}

	i.operations.Lock()
	defer i.operations.Unlock()

	select {
	case <-i.done:
		return ErrUnavailable
	default:
	}
	if err := i.admin.Load(ctx, config); err != nil {
		if errors.Is(err, ErrRejected) {
			return providergateway.ErrConfigurationRejected
		}
		return err
	}
	if err := i.verifySockets(true); err != nil {
		return ErrProtocol
	}

	return nil
}

// Done closes after Caddy and all generation capabilities are reaped.
func (i *Instance) Done() <-chan struct{} {
	if i == nil {
		done := make(chan struct{})
		close(done)
		return done
	}

	return i.done
}

// Err returns only a generic process or cleanup category.
func (i *Instance) Err() error {
	if i == nil {
		return nil
	}

	i.mu.Lock()
	defer i.mu.Unlock()

	return i.err
}

// Stop requests Caddy's native graceful stop, falling back to exact payload
// termination if the admin exchange races process exit.
func (i *Instance) Stop(ctx context.Context) error {
	if i == nil || i.background == nil {
		return nil
	}
	if ctx == nil {
		return fmt.Errorf("stop Caddy context is nil")
	}

	i.operations.Lock()
	defer i.operations.Unlock()

	adminErr := i.admin.Stop(ctx)
	select {
	case <-i.done:
		return nil
	default:
	}
	if adminErr == nil {
		select {
		case <-i.done:
			return i.stopResult(nil)
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	processErr := i.background.Stop(ctx)
	select {
	case <-i.done:
		return i.stopResult(processErr)
	case <-ctx.Done():
		return errors.Join(
			ErrUnavailable,
			processErr,
			ctx.Err(),
		)
	}
}

// Kill forces the complete Bubblewrap generation to terminate.
func (i *Instance) Kill(ctx context.Context) error {
	if i == nil || i.background == nil {
		return nil
	}
	if ctx == nil {
		return fmt.Errorf("kill Caddy context is nil")
	}

	return i.background.Kill(ctx)
}

func (i *Instance) waitReady(
	ctx context.Context,
	poll time.Duration,
) error {
	if ctx == nil {
		return ErrInvalidRequest
	}

	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		if err := i.verifySockets(false); err == nil {
			probeCtx, cancel := context.WithTimeout(
				ctx,
				min(500*time.Millisecond, poll*10),
			)
			err = i.admin.Probe(probeCtx)
			cancel()
			if err == nil {
				return nil
			}
			if !errors.Is(err, ErrUnavailable) &&
				!errors.Is(err, ErrRequestTimeout) {
				return err
			}
		}

		select {
		case <-ctx.Done():
			return ErrRequestTimeout
		case <-i.done:
			return ErrUnavailable
		case <-ticker.C:
		}
	}
}

func (i *Instance) connectAdmin(
	ctx context.Context,
) (net.Conn, error) {
	i.runtimeMu.RLock()
	defer i.runtimeMu.RUnlock()
	if i.runtime == nil {
		return nil, ErrUnavailable
	}

	if err := verifySocket(
		i.runtime,
		filepath.Base(i.adminPath),
	); err != nil {
		return nil, err
	}

	return dialSocket(
		ctx,
		i.runtime,
		filepath.Base(i.adminPath),
	)
}

func (i *Instance) verifySockets(withData bool) error {
	i.runtimeMu.RLock()
	defer i.runtimeMu.RUnlock()
	if i.runtime == nil {
		return ErrUnavailable
	}

	if err := verifySocket(
		i.runtime,
		filepath.Base(i.adminPath),
	); err != nil {
		return err
	}
	if withData {
		return verifySocket(
			i.runtime,
			filepath.Base(i.dataPath),
		)
	}

	return nil
}

func (i *Instance) stopResult(signalErr error) error {
	if i.Err() == nil {
		return nil
	}

	return errors.Join(ErrUnavailable, signalErr)
}

func (i *Instance) wait() {
	<-i.background.Done()

	processFailed := i.background.Err() != nil
	i.logger.DebugError("close Caddy admin client", i.admin.Close())

	i.runtimeMu.Lock()
	i.logger.DebugError(
		"close Caddy runtime capability",
		i.runtime.Close(),
	)
	i.runtime = nil
	i.logger.DebugError(
		"close Caddy run directories",
		i.directories.Close(),
	)
	i.runtimeMu.Unlock()

	i.mu.Lock()
	if processFailed {
		i.err = fmt.Errorf("caddy generation exited unexpectedly")
	}
	i.mu.Unlock()
	close(i.done)
}
