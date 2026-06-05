// Package mount is an fx-registered registry of the persistent named volumes and
// host binds a Toby sandbox container receives.
//
// A requester asks for a mount by Key (type/name/purpose) at a container-interior
// target path; the service resolves the configured profile internally (a global
// default plus optional per-resource overrides) and derives a stable volume name
// (toby.<profile>.<type>.<name>.<purpose>). Volumes are persistent; binds carry an
// absolute host path supplied by the caller. Each volume gets a setup path so the
// requester can hook into the root Setup phase to initialize it (the default is a
// chown to the host user).
//
// It imports nothing from internal/...; toby-sandbox path layout lives in
// container/layout.
package mount

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	pathpkg "path"
	"strings"
	"sync"

	"petris.dev/toby/container/layout"
)

// Service tracks the volumes and binds a sandbox container will receive.
type Service struct {
	mu           sync.Mutex
	configured   bool
	profile      string
	sandbox      string
	toolProfiles map[string]string
	mounts       map[Key]Entry
	ordered      []Key
	binds        []Bind
	seenBinds    map[Bind]bool
}

// Configure resets the service for a session and registers the runtime home volume.
func (s *Service) Configure(cfg Config) error {
	profile := strings.TrimSpace(cfg.Profile)
	if profile == "" {
		profile = PurposeDefault
	}

	sandboxName := strings.TrimSpace(cfg.SandboxName)
	if sandboxName == "" {
		sandboxName = PurposeDefault
	}

	s.mu.Lock()
	s.configured = true
	s.profile = profile
	s.sandbox = sandboxName
	s.toolProfiles = cloneStringMap(cfg.ToolProfiles)
	s.mounts = map[Key]Entry{}
	s.ordered = nil
	s.binds = nil
	s.seenBinds = nil
	s.mu.Unlock()

	_, err := s.AddMount(Request{Key: RuntimeHomeKey(sandboxName), Target: layout.Home})
	return err
}

// AddMount registers a persistent volume mount, returning the resolved Entry.
// Re-adding an identical mount is a no-op; conflicting settings are an error.
func (s *Service) AddMount(req Request) (Entry, error) {
	if err := validateKey(req.Key); err != nil {
		return Entry{}, err
	}

	target := layout.Expand(strings.TrimSpace(req.Target))
	if target == "" || !pathpkg.IsAbs(target) {
		return Entry{}, fmt.Errorf("mount %s target must resolve to an absolute container path", req.Key.String())
	}

	access := req.Access
	if access == "" {
		access = AccessRegular
	}

	setupPath, err := newSetupPath()
	if err != nil {
		return Entry{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.configured {
		return Entry{}, fmt.Errorf("mount service is not configured")
	}

	profile := s.getProfile(req.Key)
	mount := Entry{
		Key:       req.Key,
		Profile:   profile,
		Volume:    Volume(profile, req.Key),
		Target:    target,
		Access:    access,
		Optional:  req.Optional,
		SetupPath: setupPath,
		setup:     req.Setup,
	}

	if existing, ok := s.mounts[req.Key]; ok {
		if !sameMount(existing, mount) {
			return Entry{}, fmt.Errorf("mount %s already registered with different settings", req.Key.String())
		}
		return existing, nil
	}

	s.mounts[req.Key] = mount
	s.ordered = append(s.ordered, req.Key)
	return mount, nil
}

// AddBind registers a passthrough host bind, de-duplicating identical binds.
func (s *Service) AddBind(bind Bind) error {
	bind.Target = layout.Expand(strings.TrimSpace(bind.Target))
	if strings.TrimSpace(bind.HostPath) == "" {
		return fmt.Errorf("bind host path must not be empty")
	}
	if bind.Target == "" || !pathpkg.IsAbs(bind.Target) {
		return fmt.Errorf("bind target must resolve to an absolute container path")
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

// GetMount returns a registered mount by key.
func (s *Service) GetMount(key Key) (Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	mount, ok := s.mounts[key]
	return mount, ok
}

// GetMounts returns every registered volume mount in registration order.
func (s *Service) GetMounts() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()

	result := make([]Entry, 0, len(s.ordered))
	for _, key := range s.ordered {
		result = append(result, s.mounts[key])
	}
	return result
}

// GetBinds returns the registered passthrough binds.
func (s *Service) GetBinds() []Bind {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]Bind(nil), s.binds...)
}

// RunSetup initializes every volume's setup path during the Setup phase. Mounts
// with no custom SetupFunc are chowned to the host user in a single batch; mounts
// with a custom hook run individually.
func (s *Service) RunSetup(ctx context.Context, run Executor) error {
	s.mu.Lock()
	mounts := make([]Entry, 0, len(s.ordered))
	for _, key := range s.ordered {
		mounts = append(mounts, s.mounts[key])
	}
	s.mu.Unlock()

	var defaultPaths []string
	var custom []Entry
	for _, m := range mounts {
		if m.SetupPath == "" {
			continue
		}
		if m.setup == nil {
			defaultPaths = append(defaultPaths, m.SetupPath)
			continue
		}
		custom = append(custom, m)
	}

	if len(defaultPaths) > 0 {
		if err := defaultChown(ctx, defaultPaths, run); err != nil {
			return err
		}
	}

	for _, m := range custom {
		if err := m.setup(ctx, m.SetupPath, run); err != nil {
			return fmt.Errorf("setup mount %s: %w", m.Key.String(), err)
		}
	}

	return nil
}

func (s *Service) getProfile(key Key) string {
	if key.Type == TypeTool {
		if profile := strings.TrimSpace(s.toolProfiles[key.Name]); profile != "" {
			return profile
		}
	}
	return s.profile
}

func sameMount(a, b Entry) bool {
	return a.Key == b.Key && a.Target == b.Target && a.Access == b.Access && a.Optional == b.Optional
}

func newSetupPath() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return pathpkg.Join(layout.Root, "mounts", hex.EncodeToString(data[:])), nil
}

func cloneStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}

	clone := make(map[string]string, len(src))
	for key, value := range src {
		clone[key] = value
	}
	return clone
}
