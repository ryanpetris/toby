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
	"os"
	pathpkg "path"
	"strings"
	"sync"

	"petris.dev/toby/container/layout"

	"go.uber.org/fx"
)

const (
	TypeRuntime    = "runtime"
	TypeTool       = "tool"
	NameHome       = "home"
	PurposeDefault = "default"
)

// Access classifies the permission a mount is given inside the container.
type Access string

const (
	AccessRegular  Access = "regular"
	AccessReadOnly Access = "read_only"
	AccessDev      Access = "dev"
)

// Key uniquely identifies a managed mount.
type Key struct {
	Type    string
	Name    string
	Purpose string
}

func (k Key) String() string {
	if k.Purpose == "" {
		return k.Type + "." + k.Name
	}
	return k.Type + "." + k.Name + "." + k.Purpose
}

// RuntimeHomeKey is the key for the per-sandbox home volume.
func RuntimeHomeKey(sandboxName string) Key {
	purpose := strings.TrimSpace(sandboxName)
	if purpose == "" {
		purpose = PurposeDefault
	}
	return Key{Type: TypeRuntime, Name: NameHome, Purpose: purpose}
}

// IsRuntimeHome reports whether key is the runtime home volume.
func IsRuntimeHome(key Key) bool {
	return key.Type == TypeRuntime && key.Name == NameHome
}

// ParseKey parses "type.name" or "type.name.purpose".
func ParseKey(value string) (Key, error) {
	parts := strings.Split(strings.TrimSpace(value), ".")
	if len(parts) < 2 || len(parts) > 3 {
		return Key{}, fmt.Errorf("mount key must be type.name or type.name.purpose")
	}

	key := Key{Type: strings.TrimSpace(parts[0]), Name: strings.TrimSpace(parts[1])}
	if len(parts) == 3 {
		key.Purpose = strings.TrimSpace(parts[2])
	}
	if key.Type == "" || key.Name == "" || strings.ContainsAny(key.Type+key.Name+key.Purpose, "\x00") {
		return Key{}, fmt.Errorf("invalid mount key %q", value)
	}
	return key, nil
}

// Runner executes a command in the sandbox during the Setup phase.
type Runner interface {
	Exec(ctx context.Context, argv []string, root bool) (int, error)
}

// SetupFunc initializes a freshly-created volume as root during the Setup phase.
// setupPath is where the volume is mounted in the setup container. A nil SetupFunc
// uses the default behavior: chown the path to the host user.
type SetupFunc func(ctx context.Context, setupPath string, run Runner) error

// Request asks the service to register a persistent volume mount.
type Request struct {
	Key      Key
	Target   string // container-interior path; "~"/"~/" is expanded to the container home
	Access   Access
	Optional bool
	Setup    SetupFunc // optional; nil means default chown
}

// Bind is a passthrough host bind. HostPath must be absolute; the caller resolves it.
type Bind struct {
	HostPath string
	Target   string // container-interior path; "~"/"~/" is expanded
	Access   Access
	Optional bool
}

// Mount is a resolved persistent volume mount.
type Mount struct {
	Key       Key
	Profile   string
	Volume    string
	Target    string
	Access    Access
	Optional  bool
	SetupPath string

	setup SetupFunc
}

// Config configures the service for a single sandbox session.
type Config struct {
	Profile      string            // global default profile (namespace) for all mounts
	SandboxName  string            // names the runtime home volume's purpose
	ToolProfiles map[string]string // per-tool profile overrides, keyed by tool name
}

// Service tracks the volumes and binds a sandbox container will receive.
type Service struct {
	mu           sync.Mutex
	configured   bool
	profile      string
	sandbox      string
	toolProfiles map[string]string
	mounts       map[Key]Mount
	ordered      []Key
	binds        []Bind
	seenBinds    map[Bind]bool
}

// New constructs a Service without registering any lifecycle hook.
func New() *Service { return &Service{} }

// NewService constructs the Service for the fx graph. The lifecycle is accepted
// for house-style symmetry with the other container services; the mount service
// holds no resources that require teardown.
func NewService(lc fx.Lifecycle) *Service {
	_ = lc
	return New()
}

// Module registers the mount Service in the fx graph.
func Module() fx.Option {
	return fx.Module("mount", fx.Provide(NewService))
}

// Volume returns the stable volume name for a profile and key:
// toby.<profile>.<type>.<name>.<purpose>.
func Volume(profile string, key Key) string {
	return strings.Join([]string{
		"toby",
		namePart(profile, "default"),
		namePart(key.Type, "mount"),
		namePart(key.Name, "default"),
		namePart(key.Purpose, "default"),
	}, ".")
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
	s.mounts = map[Key]Mount{}
	s.ordered = nil
	s.binds = nil
	s.seenBinds = nil
	s.mu.Unlock()

	_, err := s.AddMount(Request{Key: RuntimeHomeKey(sandboxName), Target: layout.Home})
	return err
}

// AddMount registers a persistent volume mount, returning the resolved Mount.
// Re-adding an identical mount is a no-op; conflicting settings are an error.
func (s *Service) AddMount(req Request) (Mount, error) {
	if err := validateKey(req.Key); err != nil {
		return Mount{}, err
	}

	target := layout.Expand(strings.TrimSpace(req.Target))
	if target == "" || !pathpkg.IsAbs(target) {
		return Mount{}, fmt.Errorf("mount %s target must resolve to an absolute container path", req.Key.String())
	}

	access := req.Access
	if access == "" {
		access = AccessRegular
	}

	setupPath, err := newSetupPath()
	if err != nil {
		return Mount{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.configured {
		return Mount{}, fmt.Errorf("mount service is not configured")
	}

	profile := s.profileFor(req.Key)
	mount := Mount{
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
			return Mount{}, fmt.Errorf("mount %s already registered with different settings", req.Key.String())
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

// Mount returns a registered mount by key.
func (s *Service) Mount(key Key) (Mount, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	mount, ok := s.mounts[key]
	return mount, ok
}

// Mounts returns every registered volume mount in registration order.
func (s *Service) Mounts() []Mount {
	s.mu.Lock()
	defer s.mu.Unlock()

	result := make([]Mount, 0, len(s.ordered))
	for _, key := range s.ordered {
		result = append(result, s.mounts[key])
	}
	return result
}

// Volumes is an alias for Mounts kept for call-site clarity at the container boundary.
func (s *Service) Volumes() []Mount { return s.Mounts() }

// Binds returns the registered passthrough binds.
func (s *Service) Binds() []Bind {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]Bind(nil), s.binds...)
}

// RunSetup initializes every volume's setup path during the Setup phase. Mounts
// with no custom SetupFunc are chowned to the host user in a single batch; mounts
// with a custom hook run individually.
func (s *Service) RunSetup(ctx context.Context, run Runner) error {
	s.mu.Lock()
	mounts := make([]Mount, 0, len(s.ordered))
	for _, key := range s.ordered {
		mounts = append(mounts, s.mounts[key])
	}
	s.mu.Unlock()

	var defaultPaths []string
	var custom []Mount
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

func (s *Service) profileFor(key Key) string {
	if key.Type == TypeTool {
		if profile := strings.TrimSpace(s.toolProfiles[key.Name]); profile != "" {
			return profile
		}
	}
	return s.profile
}

func validateKey(key Key) error {
	key.Type = strings.TrimSpace(key.Type)
	key.Name = strings.TrimSpace(key.Name)
	key.Purpose = strings.TrimSpace(key.Purpose)
	if key.Type == "" || key.Name == "" || key.Purpose == "" {
		return fmt.Errorf("mount key type, name, and purpose are required")
	}
	if strings.ContainsAny(key.Type+key.Name+key.Purpose, "\x00") {
		return fmt.Errorf("mount key contains invalid NUL byte")
	}

	return nil
}

func sameMount(a, b Mount) bool {
	return a.Key == b.Key && a.Target == b.Target && a.Access == b.Access && a.Optional == b.Optional
}

func defaultChown(ctx context.Context, paths []string, run Runner) error {
	argv := append([]string{"chown", "-R", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())}, paths...)
	_, err := run.Exec(ctx, argv, true)
	return err
}

func newSetupPath() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return pathpkg.Join(layout.Root, "mounts", hex.EncodeToString(data[:])), nil
}

func namePart(value, fallback string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		if isNameChar(r) {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}

	name := strings.Trim(b.String(), "-.")
	if name == "" {
		return fallback
	}
	return name
}

func isNameChar(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '.' || r == '-'
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
