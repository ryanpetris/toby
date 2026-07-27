package providergateway

// Serializes complete Caddy configuration loads and republishes only fully
// replayed process generations.

import (
	"context"
	"errors"
	"fmt"
	"time"
)

func (s *Gateway) reconcile() {
	defer close(s.reconcileDone)

	var generation Generation
	var generationToken string
	var loadedGeneration uint64
	defer func() {
		if generation != nil {
			s.relay.unpublish(generation.Generation())
			generation.Release()
		}
	}()

	for {
		if err := s.lifetime.Err(); err != nil {
			return
		}
		if generation != nil {
			select {
			case <-generation.Done():
				s.relay.unpublish(generation.Generation())
				generation.Release()
				generation = nil
				generationToken = ""
				loadedGeneration = 0
			default:
			}
		}

		snapshot := s.routes.snapshot()
		if s.revisionFailed(snapshot.Revision) {
			if !s.waitReconcileEvent(generation, 0) {
				return
			}
			continue
		}
		if len(snapshot.Routes) == 0 {
			if generation != nil &&
				(snapshot.Revision > s.applied() ||
					loadedGeneration != generation.Generation()) {
				if generationToken == "" {
					token, err := s.options.NewToken()
					if err != nil {
						s.recordReconcileFailure(
							snapshot.Revision,
							err,
						)
						if !s.waitReconcileEvent(
							generation,
							0,
						) {
							return
						}
						continue
					}
					generationToken = token
					if err := s.routes.setGenerationToken(
						token,
					); err != nil {
						s.recordReconcileFailure(
							snapshot.Revision,
							err,
						)
						return
					}
				}

				config, err := renderCaddyConfig(
					snapshot,
					generationToken,
				)
				if err != nil {
					s.recordReconcileFailure(
						snapshot.Revision,
						err,
					)
					if !s.waitReconcileEvent(
						generation,
						0,
					) {
						return
					}
					continue
				}
				err = generation.Load(s.lifetime, config)
				if err != nil {
					delay := s.options.RetryDelay
					if errors.Is(
						err,
						ErrConfigurationRejected,
					) {
						s.recordReconcileFailure(
							snapshot.Revision,
							err,
						)
						delay = 0
					}
					if !s.waitReconcileEvent(
						generation,
						delay,
					) {
						return
					}
					continue
				}
			}

			s.confirm(snapshot.Revision)
			if generation != nil {
				s.relay.unpublish(generation.Generation())
				generation.Release()
				generation = nil
				generationToken = ""
				loadedGeneration = 0
			}
			if !s.waitReconcileEvent(nil, 0) {
				return
			}
			continue
		}

		if generation == nil {
			progress := s.progressFor(snapshot.Revision)
			acquireOperation := startProgress(
				progress,
				"caddy",
				"Acquiring Caddy gateway",
			)
			acquired, err := s.pool.Acquire(
				s.lifetime,
				progress,
			)
			if err != nil {
				acquireOperation.Fail("Caddy startup failed; retrying")
				if !s.waitReconcileEvent(
					nil,
					s.options.RetryDelay,
				) {
					return
				}
				continue
			}
			if acquired == nil ||
				acquired.Generation() == 0 {
				if acquired != nil {
					acquired.Release()
				}
				s.recordReconcileFailure(
					snapshot.Revision,
					fmt.Errorf(
						"provider Caddy pool returned an invalid generation",
					),
				)
				acquireOperation.Fail(
					"Caddy startup returned an invalid generation",
				)
				if !s.waitReconcileEvent(
					nil,
					s.options.RetryDelay,
				) {
					return
				}
				continue
			}

			generation = acquired
			acquireOperation.Complete("Caddy gateway ready")
			token, err := s.options.NewToken()
			if err != nil {
				generation.Release()
				generation = nil
				s.recordReconcileFailure(
					snapshot.Revision,
					err,
				)
				continue
			}
			if err := s.routes.setGenerationToken(token); err != nil {
				generation.Release()
				generation = nil
				s.recordReconcileFailure(
					snapshot.Revision,
					err,
				)
				continue
			}
			generationToken = token
			loadedGeneration = 0
		}

		if loadedGeneration != generation.Generation() ||
			snapshot.Revision > s.applied() {
			progress := s.progressFor(snapshot.Revision)
			loadOperation := startProgress(
				progress,
				"caddy",
				"Loading models routes into Caddy",
			)
			config, err := renderCaddyConfig(
				snapshot,
				generationToken,
			)
			if err != nil {
				s.recordReconcileFailure(
					snapshot.Revision,
					err,
				)
				loadOperation.Fail("Rendering models routes failed")
				if !s.waitReconcileEvent(generation, 0) {
					return
				}
				continue
			}
			err = generation.Load(s.lifetime, config)
			if err != nil {
				loadOperation.Fail("Caddy route load failed; retrying")
				if errors.Is(err, ErrConfigurationRejected) {
					s.recordReconcileFailure(
						snapshot.Revision,
						err,
					)
					if !s.waitReconcileEvent(
						generation,
						0,
					) {
						return
					}
					continue
				}
				if loadedGeneration != generation.Generation() {
					s.relay.unpublish(generation.Generation())
					generation.Release()
					generation = nil
					generationToken = ""
					loadedGeneration = 0
					if !s.waitReconcileEvent(
						nil,
						s.options.RetryDelay,
					) {
						return
					}
					continue
				}

				if !s.waitReconcileEvent(
					generation,
					s.options.RetryDelay,
				) {
					return
				}
				continue
			}
			if loadedGeneration != generation.Generation() {
				if err := s.relay.publish(
					generation.Generation(),
					generation.DialData,
					generation.OpenConnector,
				); err != nil {
					generation.Release()
					generation = nil
					generationToken = ""
					loadedGeneration = 0
					s.recordReconcileFailure(
						snapshot.Revision,
						err,
					)
					loadOperation.Fail("Publishing models routes failed")
					continue
				}
				loadedGeneration = generation.Generation()
			}
			loadOperation.Complete("Models routes active")
			s.confirm(snapshot.Revision)
		}

		if !s.waitReconcileEvent(generation, 0) {
			return
		}
		if generation != nil {
			select {
			case <-generation.Done():
				s.relay.unpublish(generation.Generation())
				generation.Release()
				generation = nil
				generationToken = ""
				loadedGeneration = 0
			default:
			}
		}
	}
}

func (s *Gateway) waitReconcileEvent(
	generation Generation,
	delay time.Duration,
) bool {
	var generationDone <-chan struct{}
	if generation != nil {
		generationDone = generation.Done()
	}

	if delay <= 0 {
		select {
		case <-s.lifetime.Done():
			return false
		case <-s.wake:
			return true
		case <-generationDone:
			return true
		}
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-s.lifetime.Done():
		return false
	case <-s.wake:
		return true
	case <-generationDone:
		return true
	case <-timer.C:
		return true
	}
}

func (s *Gateway) applied() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.appliedRevision
}

func (s *Gateway) revisionFailed(revision uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.failedRevision == revision &&
		s.reconcileErr != nil
}

func (s *Gateway) confirm(revision uint64) {
	s.routes.confirm(revision)

	s.mu.Lock()
	if revision > s.appliedRevision {
		s.appliedRevision = revision
	}
	if s.failedRevision <= revision {
		s.failedRevision = 0
		s.reconcileErr = nil
	}
	s.notifyLocked()
	s.mu.Unlock()
}

func (s *Gateway) recordReconcileFailure(
	revision uint64,
	err error,
) {
	s.mu.Lock()
	if revision >= s.failedRevision {
		s.failedRevision = revision
		s.reconcileErr = fmt.Errorf(
			"models gateway configuration was not applied",
		)
	}
	s.notifyLocked()
	s.mu.Unlock()
}

func (s *Gateway) waitRevision(
	ctx context.Context,
	revision uint64,
) error {
	if revision == 0 {
		return nil
	}

	for {
		s.mu.Lock()
		switch {
		case s.appliedRevision >= revision:
			s.mu.Unlock()
			return nil
		case s.terminalErr != nil:
			err := s.terminalErr
			s.mu.Unlock()
			return err
		case s.failedRevision == revision && s.reconcileErr != nil:
			err := s.reconcileErr
			s.mu.Unlock()
			return err
		case s.closing:
			s.mu.Unlock()
			return fmt.Errorf(
				"models gateway is shutting down",
			)
		}
		changed := s.changed
		s.mu.Unlock()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

func (s *Gateway) waitRemoval(
	ctx context.Context,
	revision uint64,
) error {
	if revision == 0 {
		return nil
	}

	for {
		err := s.waitRevision(ctx, revision)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}

		s.mu.Lock()
		retry := !s.closing &&
			s.terminalErr == nil &&
			s.failedRevision == revision
		if retry {
			s.failedRevision = 0
			s.reconcileErr = nil
			s.notifyLocked()
		}
		s.mu.Unlock()
		if !retry {
			return err
		}
		s.signal()
	}
}
