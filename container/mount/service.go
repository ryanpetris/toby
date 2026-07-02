// Package mount is an fx-registered registry of the one shared home volume and the
// host binds a Toby tool container receives.
//
// Under the profile-home topology there is exactly one persistent volume — the
// shared home, keyed by profile (toby.<profile>.runtime.home at /toby/home),
// created and owned by the profile's home container and mounted into the tool
// container. Tools no longer request per-tool state volumes; their state lives in
// the shared home. Binds carry an absolute host path supplied by the caller (the
// docker socket and ~/.docker) and are applied to the tool container.
//
// It imports nothing from internal/...; toby-sandbox path layout lives in
// container/layout.
package mount

import (
	pathpkg "path"
	"strings"
	"sync"

	"petris.dev/toby/container/layout"
)

// Service tracks the shared home volume's profile and the passthrough binds a tool
// container will receive.
type Service struct {
	mu        sync.Mutex
	profile   string
	binds     []Bind
	seenBinds map[Bind]bool
}

// ConfigureHome resets the service for a session and records the home profile whose
// shared home volume the tool container mounts.
func (s *Service) ConfigureHome(profile string) {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		profile = PurposeDefault
	}

	s.mu.Lock()
	s.profile = profile
	s.binds = nil
	s.seenBinds = nil
	s.mu.Unlock()
}

// Profile returns the configured home profile (PurposeDefault before configuration).
func (s *Service) Profile() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.profile == "" {
		return PurposeDefault
	}
	return s.profile
}

// HomeVolume returns the shared home volume name for the configured profile.
func (s *Service) HomeVolume() string {
	return HomeVolume(s.Profile())
}

// AddBind registers a passthrough host bind, de-duplicating identical binds.
func (s *Service) AddBind(bind Bind) error {
	bind.Target = layout.Expand(strings.TrimSpace(bind.Target))
	if strings.TrimSpace(bind.HostPath) == "" {
		return errEmptyHostPath
	}
	if bind.Target == "" || !pathpkg.IsAbs(bind.Target) {
		return errBindTarget
	}
	if bind.Access == "" {
		bind.Access = AccessRegular
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.seenBinds == nil {
		s.seenBinds = map[Bind]bool{}
	}
	if s.seenBinds[bind] {
		return nil
	}

	s.seenBinds[bind] = true
	s.binds = append(s.binds, bind)
	return nil
}

// Binds returns the registered passthrough binds in registration order.
func (s *Service) Binds() []Bind {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]Bind(nil), s.binds...)
}
