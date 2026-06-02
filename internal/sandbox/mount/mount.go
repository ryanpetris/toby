package mount

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"

	sandboxpath "petris.dev/toby/internal/sandbox/path"
)

const (
	TypeRuntime    = "runtime"
	TypeTool       = "tool"
	NameHome       = "home"
	PurposeDefault = "default"
)

func RuntimeHomeKey(sandboxName string) Key {
	purpose := strings.TrimSpace(sandboxName)
	if purpose == "" {
		purpose = PurposeDefault
	}
	return Key{Type: TypeRuntime, Name: NameHome, Purpose: purpose}
}

type Key struct {
	Type    string
	Name    string
	Purpose string
}

type Access string

const (
	AccessRegular  Access = "regular"
	AccessReadOnly Access = "read_only"
	AccessDev      Access = "dev"
)

type Bind struct {
	HostPath string
	Target   sandboxpath.Target
	Access   Access
	Optional bool
}

type Backing string

const (
	BackingDefault  Backing = "default"
	BackingProvider Backing = "provider"
	BackingPrivate  Backing = "private"
	BackingHost     Backing = "host"
)

type BackingConfig struct {
	Backing  Backing
	HostRoot string
}

type Profiles map[string]BackingConfig

type Config struct {
	Profile      string
	SandboxName  string
	Paths        sandboxpath.Paths
	Profiles     Profiles
	ToolProfiles map[string]string
}

type Request struct {
	Key      Key
	Target   sandboxpath.Target
	Subpath  string
	Access   Access
	Optional bool
}

type SourceKind string

const (
	SourceHostPath SourceKind = "host_path"
	SourceProvider SourceKind = "provider"
)

type Source struct {
	Kind  SourceKind
	Value string
}

type Info struct {
	Key        Key
	Profile    string
	ProviderID string
	Backing    Backing
	Target     string
	Subpath    string
	Active     bool
	Source     Source
	SetupPath  string
	Access     Access
	Optional   bool
}

type RuntimeMount struct {
	Key        Key
	ProviderID string
	Source     Source
	Target     string
	SetupPath  string
	Access     Access
	Optional   bool
}

type Service struct {
	configured   bool
	profile      string
	sandbox      string
	paths        sandboxpath.Paths
	profiles     Profiles
	toolProfiles map[string]string
	mounts       map[Key]Info
	ordered      []Key
}

func NewService() *Service { return &Service{} }

func ParseBacking(value string) (Backing, error) {
	switch backing := Backing(strings.TrimSpace(value)); backing {
	case BackingDefault, BackingProvider, BackingPrivate, BackingHost:
		return backing, nil
	default:
		return "", fmt.Errorf("mount backing must be %q, %q, %q, or %q", BackingDefault, BackingProvider, BackingPrivate, BackingHost)
	}
}

func (p Profiles) Clone() Profiles {
	if len(p) == 0 {
		return nil
	}
	clone := make(Profiles, len(p))
	for name, cfg := range p {
		clone[name] = cfg
	}
	return clone
}

func (p *Profiles) Merge(src Profiles) {
	if len(src) == 0 {
		return
	}
	if *p == nil {
		*p = Profiles{}
	}
	for name, srcSettings := range src {
		profile := namePart(name, PurposeDefault)
		cfg := (*p)[profile]
		cfg.merge(srcSettings)
		(*p)[profile] = cfg
	}
}

func (p Profiles) Config(name string) BackingConfig {
	profile := namePart(name, PurposeDefault)
	return p[profile]
}

func (c BackingConfig) BackingFor(key Key) Backing {
	cfg := c.ConfigFor(key)
	if cfg.Backing != "" && cfg.Backing != BackingDefault {
		return cfg.Backing
	}
	return BackingProvider
}

func (c BackingConfig) HostRootFor(key Key) string {
	cfg := c.ConfigFor(key)
	if cfg.HostRoot != "" {
		return cfg.HostRoot
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return ""
}

func (c BackingConfig) ConfigFor(key Key) BackingConfig {
	if IsRuntimeHome(key) {
		return BackingConfig{Backing: BackingProvider}
	}
	return c
}

func (p Profiles) ResolveHostRoots(home, base string) (Profiles, error) {
	resolved := p.Clone()
	for name, cfg := range resolved {
		profile, err := cfg.ResolveHostRoots(home, base)
		if err != nil {
			return nil, err
		}
		resolved[name] = profile
	}
	return resolved, nil
}

func (c BackingConfig) ResolveHostRoots(home, base string) (BackingConfig, error) {
	if c.HostRoot == "" {
		return c, nil
	}
	root, err := ResolveHostRoot(c.HostRoot, home, base)
	if err != nil {
		return BackingConfig{}, err
	}
	c.HostRoot = root
	return c, nil
}

func ResolveHostRoot(value, home, base string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("hostRoot must not be empty")
	}
	value = expandHome(value, home)
	if filepath.IsAbs(value) {
		return value, nil
	}
	if base == "" {
		base = "."
	}
	return filepath.Join(base, value), nil
}

func (s *Service) Configure(cfg Config) error {
	profile := strings.TrimSpace(cfg.Profile)
	if profile == "" {
		profile = PurposeDefault
	}
	sandboxName := strings.TrimSpace(cfg.SandboxName)
	if sandboxName == "" {
		sandboxName = PurposeDefault
	}
	s.configured = true
	s.profile = profile
	s.sandbox = sandboxName
	s.paths = cfg.Paths
	s.profiles = cfg.Profiles.Clone()
	s.toolProfiles = cloneStringMap(cfg.ToolProfiles)
	s.mounts = map[Key]Info{}
	s.ordered = nil
	_, err := s.Add(Request{Key: RuntimeHomeKey(sandboxName), Target: sandboxpath.HomePath()})
	return err
}

func (s *Service) Add(req Request) (Info, error) {
	if !s.configured {
		return Info{}, fmt.Errorf("mount service is not configured")
	}
	if err := validateRequest(req); err != nil {
		return Info{}, err
	}
	info, err := s.resolve(req)
	if err != nil {
		return Info{}, err
	}
	if existing, ok := s.mounts[req.Key]; ok {
		if !sameMount(existing, info) {
			return Info{}, fmt.Errorf("mount %s already registered with different settings", req.Key.String())
		}
		return existing, nil
	}
	s.mounts[req.Key] = info
	s.ordered = append(s.ordered, req.Key)
	return info, nil
}

func (s *Service) Get(key Key) (Info, bool) {
	info, ok := s.mounts[key]
	return info, ok
}

func (s *Service) RuntimeMounts() []RuntimeMount {
	result := make([]RuntimeMount, 0, len(s.ordered))
	for _, key := range s.ordered {
		info := s.mounts[key]
		if !info.Active {
			continue
		}
		result = append(result, RuntimeMount{Key: info.Key, ProviderID: info.ProviderID, Source: info.Source, Target: info.Target, SetupPath: info.SetupPath, Access: info.Access, Optional: info.Optional})
	}
	return result
}

func (s *Service) HostBackedManagedMounts() []Info {
	var result []Info
	for _, key := range s.ordered {
		info := s.mounts[key]
		if info.Active && info.Backing == BackingHost && info.Key.Type == TypeTool {
			result = append(result, info)
		}
	}
	return result
}

func (s *Service) PrepareHostMounts() error {
	for _, key := range s.ordered {
		info := s.mounts[key]
		if !info.Active || info.Source.Kind != SourceHostPath {
			continue
		}
		if strings.TrimSpace(info.Source.Value) == "" {
			return fmt.Errorf("host-backed mount %s source is empty", info.Key.String())
		}
		if err := os.MkdirAll(info.Source.Value, 0o755); err != nil {
			return fmt.Errorf("prepare host-backed mount %s at %s: %w", info.Key.String(), info.Source.Value, err)
		}
	}
	return nil
}

func ProviderID(profile string, key Key) string {
	return strings.Join([]string{"toby", namePart(profile, "default"), namePart(key.Type, "mount"), namePart(key.Name, "default"), namePart(key.Purpose, "default")}, ".")
}

func IsRuntimeHome(key Key) bool {
	return key.Type == TypeRuntime && key.Name == NameHome
}

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

func (k Key) String() string {
	if k.Purpose == "" {
		return k.Type + "." + k.Name
	}
	return k.Type + "." + k.Name + "." + k.Purpose
}

func (s *Service) resolve(req Request) (Info, error) {
	access := req.Access
	if access == "" {
		access = AccessRegular
	}
	profile := s.profileFor(req.Key)
	cfg := s.profiles.Config(profile)
	info := Info{Key: req.Key, Profile: profile, Target: sandboxpath.Resolve(req.Target, s.paths), Subpath: defaultSubpath(req), Access: access, Optional: req.Optional}
	backing := cfg.BackingFor(req.Key)
	info.Backing = backing
	switch backing {
	case BackingPrivate:
		return info, nil
	case BackingHost:
		hostRoot := cfg.HostRootFor(req.Key)
		info.Active = true
		info.Source = Source{Kind: SourceHostPath, Value: filepath.Join(hostRoot, filepath.FromSlash(info.Subpath))}
		return info, nil
	case BackingProvider:
		providerID := ProviderID(profile, req.Key)
		setupPath, err := newSetupPath()
		if err != nil {
			return Info{}, err
		}
		info.Active = true
		info.ProviderID = providerID
		info.Source = Source{Kind: SourceProvider, Value: providerID}
		info.SetupPath = setupPath
		return info, nil
	default:
		return Info{}, fmt.Errorf("unsupported mount backing %q", backing)
	}
}

func (s *Service) profileFor(key Key) string {
	if key.Type == TypeTool {
		if profile := strings.TrimSpace(s.toolProfiles[key.Name]); profile != "" {
			return profile
		}
	}
	return s.profile
}

func validateRequest(req Request) error {
	if err := validateKey(req.Key); err != nil {
		return err
	}
	if target := sandboxpath.Resolve(req.Target, sandboxpath.Defaults()); strings.TrimSpace(target) == "" || !pathpkg.IsAbs(target) {
		return fmt.Errorf("mount %s target must resolve to an absolute sandbox path", req.Key.String())
	}
	return nil
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

func defaultSubpath(req Request) string {
	if req.Subpath != "" {
		return filepath.ToSlash(req.Subpath)
	}
	if req.Target.Base == sandboxpath.Home {
		return req.Target.Path
	}
	return ""
}

func sameMount(a, b Info) bool {
	return a.Key == b.Key && a.Backing == b.Backing && a.Target == b.Target && a.Subpath == b.Subpath && a.Active == b.Active && a.Source == b.Source && a.Access == b.Access && a.Optional == b.Optional
}

func (c *BackingConfig) merge(src BackingConfig) {
	if src.Backing != "" {
		c.Backing = src.Backing
	}
	if src.HostRoot != "" {
		c.HostRoot = src.HostRoot
	}
}

func newSetupPath() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return filepath.ToSlash(filepath.Join(sandboxpath.DefaultRoot, "mounts", hex.EncodeToString(data[:]))), nil
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

func expandHome(value, home string) string {
	if value == "~" {
		return home
	}
	if strings.HasPrefix(value, "~/") {
		return home + value[1:]
	}
	return value
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
