package resource

// Canonicalizes resource specifications and replaces sensitive environment
// values with agent-keyed HMAC fingerprints before hashing.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const minimumHMACKeyBytes = 32

var (
	componentPattern   = regexp.MustCompile(`^[a-z][a-z0-9._-]*$`)
	digestPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9+._-]*:[0-9a-f]+$`)
	environmentPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// Builder validates resource specifications and computes canonical identities.
type Builder struct {
	hmacKey []byte
}

// NewBuilder constructs a resource identity builder with an HMAC key.
func NewBuilder(hmacKey []byte) (*Builder, error) {
	if len(hmacKey) < minimumHMACKeyBytes {
		return nil, fmt.Errorf("resource HMAC key must be at least %d bytes", minimumHMACKeyBytes)
	}
	return &Builder{hmacKey: append([]byte(nil), hmacKey...)}, nil
}

type canonicalSpec struct {
	SchemaVersion   int                    `json:"schema_version"`
	Kind            Kind                   `json:"kind"`
	Transport       Transport              `json:"transport"`
	ManifestDigest  string                 `json:"manifest_digest"`
	RootFSDigest    string                 `json:"rootfs_digest"`
	Argv            []string               `json:"argv"`
	Workdir         string                 `json:"workdir"`
	Identity        canonicalIdentity      `json:"identity"`
	Environment     []canonicalEnvironment `json:"environment"`
	Endpoint        Endpoint               `json:"endpoint"`
	Mounts          []Mount                `json:"mounts"`
	Network         Network                `json:"network"`
	IdleTimeoutNS   int64                  `json:"idle_timeout_ns"`
	BridgeVersion   string                 `json:"bridge_version"`
	ProtocolVersion string                 `json:"protocol_version"`
	RunAuthority    RunAuthority           `json:"run_authority"`
	Scope           Scope                  `json:"scope"`
	ScopeIdentity   string                 `json:"scope_identity"`
}

type canonicalIdentity struct {
	UID int `json:"uid"`
	GID int `json:"gid"`
}

type canonicalEnvironment struct {
	Name              string `json:"name"`
	Value             string `json:"value,omitempty"`
	SecretFingerprint string `json:"secret_fingerprint,omitempty"`
}

// Build validates spec and returns its canonical resource key.
func (b *Builder) Build(spec Spec) (Key, error) {
	canonical, err := b.canonicalize(spec)
	if err != nil {
		return Key{}, err
	}
	document, err := json.Marshal(canonical)
	if err != nil {
		return Key{}, fmt.Errorf("marshal canonical resource identity: %w", err)
	}

	return Key{
		digest:    sha256.Sum256(document),
		kind:      canonical.Kind,
		transport: canonical.Transport,
		scope:     canonical.Scope,
	}, nil
}

func (b *Builder) canonicalize(spec Spec) (canonicalSpec, error) {
	if err := validateUTF8(spec); err != nil {
		return canonicalSpec{}, err
	}
	if !componentPattern.MatchString(string(spec.Kind)) {
		return canonicalSpec{}, fmt.Errorf("invalid resource kind %q", spec.Kind)
	}
	switch spec.Kind {
	case KindMCPStdio, KindMCPHTTP, KindCaddy:
	default:
		return canonicalSpec{}, fmt.Errorf("unsupported resource kind %q", spec.Kind)
	}
	if !componentPattern.MatchString(string(spec.Transport)) {
		return canonicalSpec{}, fmt.Errorf("invalid resource transport %q", spec.Transport)
	}
	switch spec.Transport {
	case TransportStdio, TransportHTTP:
	default:
		return canonicalSpec{}, fmt.Errorf("unsupported resource transport %q", spec.Transport)
	}
	if (spec.Kind == KindMCPStdio) != (spec.Transport == TransportStdio) {
		return canonicalSpec{}, fmt.Errorf("resource kind %q is incompatible with transport %q", spec.Kind, spec.Transport)
	}
	if !digestPattern.MatchString(spec.ManifestDigest) {
		return canonicalSpec{}, fmt.Errorf("invalid manifest digest %q", spec.ManifestDigest)
	}
	if !digestPattern.MatchString(spec.RootFSDigest) {
		return canonicalSpec{}, fmt.Errorf("invalid rootfs digest %q", spec.RootFSDigest)
	}
	if len(spec.Argv) == 0 || strings.TrimSpace(spec.Argv[0]) == "" {
		return canonicalSpec{}, fmt.Errorf("resource argv must contain a command")
	}
	for i, arg := range spec.Argv {
		if strings.ContainsRune(arg, '\x00') {
			return canonicalSpec{}, fmt.Errorf("resource argv[%d] contains NUL", i)
		}
	}
	if !filepath.IsAbs(spec.Workdir) || filepath.Clean(spec.Workdir) != spec.Workdir {
		return canonicalSpec{}, fmt.Errorf("resource workdir must be a clean absolute path: %q", spec.Workdir)
	}
	if spec.Identity.UID < 0 || spec.Identity.GID < 0 {
		return canonicalSpec{}, fmt.Errorf("resource UID and GID must be non-negative")
	}
	if spec.BridgeVersion == "" || spec.ProtocolVersion == "" {
		return canonicalSpec{}, fmt.Errorf("resource bridge and protocol versions are required")
	}
	if spec.Network != NetworkHost && spec.Network != NetworkPrivate {
		return canonicalSpec{}, fmt.Errorf("invalid resource network %q", spec.Network)
	}
	if spec.IdleTimeout < 0 {
		return canonicalSpec{}, fmt.Errorf(
			"resource idle timeout must not be negative",
		)
	}

	endpoint, err := canonicalEndpoint(spec.Endpoint, spec.Transport)
	if err != nil {
		return canonicalSpec{}, err
	}
	environment, err := b.canonicalizeEnvironment(spec.Environment)
	if err != nil {
		return canonicalSpec{}, err
	}
	mounts, err := canonicalizeMounts(spec.Mounts)
	if err != nil {
		return canonicalSpec{}, err
	}
	effectiveScope, err := EffectiveScope(spec.RequestedScope, mounts, spec.RunAuthority)
	if err != nil {
		return canonicalSpec{}, err
	}
	scopeRank, err := effectiveScope.rank()
	if err != nil {
		return canonicalSpec{}, err
	}
	if scopeRank == 0 && spec.ScopeIdentity != "" {
		return canonicalSpec{}, fmt.Errorf("user-scoped resource must not have a scope identity")
	}
	if scopeRank > 0 && strings.TrimSpace(spec.ScopeIdentity) == "" {
		return canonicalSpec{}, fmt.Errorf("%s-scoped resource requires a scope identity", effectiveScope)
	}
	if strings.ContainsRune(spec.ScopeIdentity, '\x00') {
		return canonicalSpec{}, fmt.Errorf("resource scope identity contains NUL")
	}

	return canonicalSpec{
		SchemaVersion:  1,
		Kind:           spec.Kind,
		Transport:      spec.Transport,
		ManifestDigest: spec.ManifestDigest,
		RootFSDigest:   spec.RootFSDigest,
		Argv:           append([]string(nil), spec.Argv...),
		Workdir:        spec.Workdir,
		Identity: canonicalIdentity{
			UID: spec.Identity.UID,
			GID: spec.Identity.GID,
		},
		Environment:     environment,
		Endpoint:        endpoint,
		Mounts:          mounts,
		Network:         spec.Network,
		IdleTimeoutNS:   int64(spec.IdleTimeout),
		BridgeVersion:   spec.BridgeVersion,
		ProtocolVersion: spec.ProtocolVersion,
		RunAuthority:    spec.RunAuthority,
		Scope:           effectiveScope,
		ScopeIdentity:   spec.ScopeIdentity,
	}, nil
}

func validateUTF8(spec Spec) error {
	values := []struct {
		name  string
		value string
	}{
		{name: "kind", value: string(spec.Kind)},
		{name: "transport", value: string(spec.Transport)},
		{name: "manifest digest", value: spec.ManifestDigest},
		{name: "rootfs digest", value: spec.RootFSDigest},
		{name: "workdir", value: spec.Workdir},
		{name: "endpoint kind", value: string(spec.Endpoint.Kind)},
		{name: "endpoint socket", value: spec.Endpoint.Socket},
		{name: "endpoint path", value: spec.Endpoint.Path},
		{name: "network", value: string(spec.Network)},
		{name: "bridge version", value: spec.BridgeVersion},
		{name: "protocol version", value: spec.ProtocolVersion},
		{name: "requested scope", value: string(spec.RequestedScope)},
		{name: "run authority", value: string(spec.RunAuthority)},
		{name: "scope identity", value: spec.ScopeIdentity},
	}
	for _, value := range values {
		if !utf8.ValidString(value.value) {
			return fmt.Errorf("resource %s contains invalid UTF-8", value.name)
		}
	}
	for i, arg := range spec.Argv {
		if !utf8.ValidString(arg) {
			return fmt.Errorf("resource argv[%d] contains invalid UTF-8", i)
		}
	}
	for i, variable := range spec.Environment {
		if !utf8.ValidString(variable.Name) {
			return fmt.Errorf("resource environment[%d] name contains invalid UTF-8", i)
		}
		if !utf8.ValidString(variable.Value) {
			return fmt.Errorf("resource environment[%d] value contains invalid UTF-8", i)
		}
	}
	for i, mount := range spec.Mounts {
		mountValues := []struct {
			name  string
			value string
		}{
			{name: "source", value: mount.Source},
			{name: "target", value: mount.Target},
			{name: "access", value: mount.Access},
			{name: "scope", value: string(mount.Scope)},
		}
		for _, value := range mountValues {
			if !utf8.ValidString(value.value) {
				return fmt.Errorf("resource mount[%d] %s contains invalid UTF-8", i, value.name)
			}
		}
	}

	return nil
}

func canonicalEndpoint(endpoint Endpoint, transport Transport) (Endpoint, error) {
	switch endpoint.Kind {
	case EndpointNone:
		if endpoint.Port != 0 ||
			endpoint.Socket != "" ||
			endpoint.Path != "" {
			return Endpoint{}, fmt.Errorf(
				"endpoint kind none must not have a port, socket, or path",
			)
		}
		if transport == TransportHTTP {
			return Endpoint{}, fmt.Errorf("HTTP resource requires a TCP or Unix endpoint")
		}
	case EndpointTCP:
		if endpoint.Port == 0 {
			return Endpoint{}, fmt.Errorf("TCP endpoint requires a port")
		}
		if endpoint.Socket != "" {
			return Endpoint{}, fmt.Errorf(
				"TCP endpoint must not have a socket",
			)
		}
		if endpoint.Path == "" || endpoint.Path[0] != '/' {
			return Endpoint{}, fmt.Errorf("TCP endpoint path must start with /")
		}
	case EndpointUnix:
		if endpoint.Port != 0 {
			return Endpoint{}, fmt.Errorf("unix endpoint must not have a port")
		}
		if !filepath.IsAbs(endpoint.Socket) ||
			filepath.Clean(endpoint.Socket) != endpoint.Socket {
			return Endpoint{}, fmt.Errorf(
				"unix endpoint socket must be a clean absolute path",
			)
		}
		if endpoint.Path == "" || endpoint.Path[0] != '/' {
			return Endpoint{}, fmt.Errorf(
				"unix endpoint protocol path must start with /",
			)
		}
	default:
		return Endpoint{}, fmt.Errorf("invalid endpoint kind %q", endpoint.Kind)
	}
	return endpoint, nil
}

func (b *Builder) canonicalizeEnvironment(environment []EnvironmentVariable) ([]canonicalEnvironment, error) {
	result := make([]canonicalEnvironment, len(environment))
	for i, variable := range environment {
		if !environmentPattern.MatchString(variable.Name) {
			return nil, fmt.Errorf("invalid environment name %q", variable.Name)
		}
		if strings.ContainsRune(variable.Value, '\x00') {
			return nil, fmt.Errorf("environment %q contains NUL", variable.Name)
		}

		result[i].Name = variable.Name
		if variable.Sensitive {
			mac := hmac.New(sha256.New, b.hmacKey)
			if _, err := mac.Write([]byte(variable.Value)); err != nil {
				return nil, fmt.Errorf(
					"fingerprint sensitive environment %q: %w",
					variable.Name,
					err,
				)
			}
			result[i].SecretFingerprint = hex.EncodeToString(mac.Sum(nil))
		} else {
			result[i].Value = variable.Value
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	for i := 1; i < len(result); i++ {
		if result[i].Name == result[i-1].Name {
			return nil, fmt.Errorf("duplicate environment name %q", result[i].Name)
		}
	}
	return result, nil
}

func canonicalizeMounts(mounts []Mount) ([]Mount, error) {
	result := append([]Mount(nil), mounts...)
	for i := range result {
		mount := &result[i]
		if !filepath.IsAbs(mount.Source) || filepath.Clean(mount.Source) != mount.Source {
			return nil, fmt.Errorf("mount source must be a clean absolute path: %q", mount.Source)
		}
		if !filepath.IsAbs(mount.Target) || filepath.Clean(mount.Target) != mount.Target {
			return nil, fmt.Errorf("mount target must be a clean absolute path: %q", mount.Target)
		}
		if mount.Access != "regular" &&
			mount.Access != "read_only" &&
			mount.Access != "dev" {
			return nil, fmt.Errorf("invalid mount access %q", mount.Access)
		}
		if _, err := mount.Scope.rank(); err != nil {
			return nil, fmt.Errorf("mount %q: %w", mount.Target, err)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := result[i], result[j]
		if left.Target != right.Target {
			return left.Target < right.Target
		}
		if left.Source != right.Source {
			return left.Source < right.Source
		}
		if left.SourceIdentity.Device != right.SourceIdentity.Device {
			return left.SourceIdentity.Device <
				right.SourceIdentity.Device
		}
		if left.SourceIdentity.Inode != right.SourceIdentity.Inode {
			return left.SourceIdentity.Inode <
				right.SourceIdentity.Inode
		}
		if left.SourceIdentity.FileType != right.SourceIdentity.FileType {
			return left.SourceIdentity.FileType <
				right.SourceIdentity.FileType
		}
		if left.Access != right.Access {
			return left.Access < right.Access
		}
		return left.Scope < right.Scope
	})
	for i := 1; i < len(result); i++ {
		if result[i].Target == result[i-1].Target {
			return nil, fmt.Errorf("duplicate resource mount target %q", result[i].Target)
		}
	}
	return result, nil
}
