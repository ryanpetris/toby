package opencodeconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"petris.dev/toby/fusekit"
	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/openai"
	"petris.dev/toby/internal/staticfiles"
	"petris.dev/toby/internal/staticmount"

	"gopkg.in/yaml.v3"
)

const (
	DirPath                 = "/opencode"
	ConfigPath              = "/opencode/opencode.json"
	CommandsPath            = "/opencode/commands"
	ProjectMountCommandPath = "/opencode/commands/toby.project.mount.md"
	StaticGitignorePath     = "opencode/.gitignore"
	StaticConfigPath        = "opencode/opencode.json"
	StaticCommandsPath      = "opencode/commands"
	StaticProjectMountPath  = "opencode/commands/toby.project.mount.md"
	maxConfig               = 1 << 20
)

var opencodeGitignore = []byte("*\n")

var projectMountCommand = []byte(`---
description: Mount a Toby project directory
---
Use the Toby MCP project_mount tool to request access to the project named "$ARGUMENTS".

If no project name was provided, ask which project directory under XDG_PROJECTS_DIR should be mounted.
After the tool succeeds, use the returned sandbox_path for subsequent work.
`)

type sourceFormat string

const (
	formatJSON sourceFormat = "json"
	formatYAML sourceFormat = "yaml"
)

type sourceFile struct {
	path   string
	format sourceFormat
}

var substitutionPattern = regexp.MustCompile(`\{(env|file):([^}]+)\}`)

type Mount struct {
	configDir         string
	projectRoot       string
	instructions      []string
	mountableProjects bool
	http              *http.Client
	modelsOnce        sync.Once
	models            map[string]map[string]any
	modelWarnings     []error
	created           time.Time
}

type MountOption func(*Mount)

func WithModelHTTPClient(client *http.Client) MountOption {
	return func(m *Mount) {
		if client != nil {
			m.http = client
		}
	}
}

func WithMountableProjects(enabled bool) MountOption {
	return func(m *Mount) {
		m.mountableProjects = enabled
	}
}

func NewMount(configDir, projectRoot string, instructions []string, opts ...MountOption) *Mount {
	mount := &Mount{configDir: configDir, projectRoot: projectRoot, instructions: append([]string(nil), instructions...), http: &http.Client{Timeout: 30 * time.Second}, created: time.Now()}
	for _, opt := range opts {
		opt(mount)
	}
	return mount
}

func StaticFiles(ctx context.Context, configDir, projectRoot string, instructions []string, opts ...MountOption) ([]staticmount.File, []error, error) {
	builder := staticfiles.NewService().NewBuilder()
	warnings, err := RegisterStaticFiles(ctx, builder, configDir, projectRoot, instructions, opts...)
	if err != nil {
		return nil, nil, err
	}
	return builder.Files(), warnings, nil
}

func RegisterStaticFiles(ctx context.Context, registrar staticfiles.Registrar, configDir, projectRoot string, instructions []string, opts ...MountOption) ([]error, error) {
	mount := NewMount(configDir, projectRoot, instructions, opts...)
	config, err := mount.render(ctx)
	if err != nil {
		return nil, err
	}
	if err := registrar.AddBytes(StaticGitignorePath, opencodeGitignore, 0o400); err != nil {
		return nil, err
	}
	if err := registrar.AddBytes(StaticConfigPath, config, 0o400); err != nil {
		return nil, err
	}
	if mount.mountableProjects {
		if err := registrar.AddBytes(StaticProjectMountPath, projectMountCommand, 0o400); err != nil {
			return nil, err
		}
	}
	return append([]error(nil), mount.modelWarnings...), nil
}

func (m *Mount) ID() string { return "opencode-config" }

func (m *Mount) BasePath() string { return DirPath }

func (m *Mount) PrepareCrossMountRename(_ context.Context, op fusekit.Operation) (bool, error) {
	return false, nil
}

func (m *Mount) Handle(ctx context.Context, op fusekit.Operation, next fusekit.Next) (fusekit.Result, error) {
	if op.Kind == fusekit.OpRename {
		if m.isSyntheticPath(op.OldPath) || m.isSyntheticPath(op.NewPath) {
			return fusekit.Result{}, syscall.EROFS
		}
		return next(ctx, op)
	}
	if op.Path == DirPath {
		switch op.Kind {
		case fusekit.OpGetAttr:
			return m.getDirAttr()
		case fusekit.OpReadDir:
			return m.readDir(ctx, op, next)
		case fusekit.OpMaterialize:
			return fusekit.Result{}, nil
		default:
			return next(ctx, op)
		}
	}
	if op.Path == CommandsPath {
		if !m.mountableProjects {
			return nextOrENOENT(ctx, op, next)
		}
		switch op.Kind {
		case fusekit.OpGetAttr:
			return m.dirAttr(CommandsPath, 0o500), nil
		case fusekit.OpReadDir:
			return fusekit.Result{Entries: []fusekit.DirEntry{{Name: filepath.Base(ProjectMountCommandPath), Object: fusekit.ObjectKey{MountID: m.ID(), Kind: "file", Key: ProjectMountCommandPath}, Mode: syscall.S_IFREG | 0o400}}}, nil
		case fusekit.OpMaterialize:
			return fusekit.Result{}, nil
		default:
			return fusekit.Result{}, syscall.EROFS
		}
	}
	if op.Path == ProjectMountCommandPath {
		if !m.mountableProjects {
			return nextOrENOENT(ctx, op, next)
		}
		return m.handleReadOnlyFile(ctx, op, ProjectMountCommandPath, projectMountCommand, 0o400)
	}
	if op.Path != ConfigPath {
		return next(ctx, op)
	}
	switch op.Kind {
	case fusekit.OpGetAttr:
		return m.getAttr(ctx)
	case fusekit.OpOpen:
		if hasWriteFlags(op.Flags) {
			return fusekit.Result{}, syscall.EROFS
		}
		return m.open(ctx, op.Flags)
	case fusekit.OpCreate:
		return fusekit.Result{}, syscall.EROFS
	case fusekit.OpSetAttr:
		return fusekit.Result{}, syscall.EROFS
	case fusekit.OpUnlink:
		return fusekit.Result{}, syscall.EROFS
	case fusekit.OpMkdir, fusekit.OpRmdir, fusekit.OpSymlink, fusekit.OpMaterialize:
		return fusekit.Result{}, syscall.ENOTDIR
	default:
		return next(ctx, op)
	}
}

func nextOrENOENT(ctx context.Context, op fusekit.Operation, next fusekit.Next) (fusekit.Result, error) {
	if next == nil {
		return fusekit.Result{}, syscall.ENOENT
	}
	return next(ctx, op)
}

func (m *Mount) isSyntheticPath(path string) bool {
	if path == ConfigPath {
		return true
	}
	if !m.mountableProjects {
		return false
	}
	return path == CommandsPath || path == ProjectMountCommandPath || strings.HasPrefix(path, CommandsPath+"/")
}

func (m *Mount) getDirAttr() (fusekit.Result, error) {
	return m.dirAttr(DirPath, 0o500), nil
}

func (m *Mount) dirAttr(path string, mode uint32) fusekit.Result {
	attr := fusekit.Attr{
		Object: fusekit.ObjectKey{MountID: m.ID(), Kind: "dir", Key: path},
		Mode:   syscall.S_IFDIR | mode,
		UID:    uint32(os.Getuid()),
		GID:    uint32(os.Getgid()),
		Nlink:  2,
		ATime:  m.created,
		MTime:  m.created,
		CTime:  m.created,
	}
	return fusekit.Result{Attr: &attr}
}

func (m *Mount) readDir(ctx context.Context, op fusekit.Operation, next fusekit.Next) (fusekit.Result, error) {
	seen := map[string]bool{}
	entries := []fusekit.DirEntry{}
	if next != nil {
		res, err := next(ctx, op)
		if err != nil {
			if fusekit.ErrnoOf(err) != syscall.ENOENT {
				return fusekit.Result{}, err
			}
		} else {
			for _, entry := range res.Entries {
				seen[entry.Name] = true
				entries = append(entries, entry)
			}
		}
	}
	if !seen["opencode.json"] {
		entries = append(entries, fusekit.DirEntry{
			Name:   "opencode.json",
			Object: fusekit.ObjectKey{MountID: m.ID(), Kind: "file", Key: ConfigPath},
			Mode:   syscall.S_IFREG | 0o400,
		})
	}
	if m.mountableProjects && !seen["commands"] {
		entries = append(entries, fusekit.DirEntry{
			Name:   "commands",
			Object: fusekit.ObjectKey{MountID: m.ID(), Kind: "dir", Key: CommandsPath},
			Mode:   syscall.S_IFDIR | 0o500,
		})
	}
	return fusekit.Result{Entries: entries}, nil
}

func (m *Mount) getAttr(ctx context.Context) (fusekit.Result, error) {
	data, err := m.render(ctx)
	if err != nil {
		return fusekit.Result{}, err
	}
	attr := m.attr(uint64(len(data)))
	return fusekit.Result{Attr: &attr}, nil
}

func (m *Mount) open(ctx context.Context, flags uint32) (fusekit.Result, error) {
	data, err := m.render(ctx)
	if err != nil {
		return fusekit.Result{}, err
	}
	attr := m.attr(uint64(len(data)))
	return fusekit.Result{Attr: &attr, Handle: &readOnlyFile{data: append([]byte(nil), data...)}}, nil
}

func (m *Mount) handleReadOnlyFile(ctx context.Context, op fusekit.Operation, path string, data []byte, mode uint32) (fusekit.Result, error) {
	switch op.Kind {
	case fusekit.OpGetAttr:
		attr := m.fileAttr(path, uint64(len(data)), mode)
		return fusekit.Result{Attr: &attr}, nil
	case fusekit.OpOpen:
		if hasWriteFlags(op.Flags) {
			return fusekit.Result{}, syscall.EROFS
		}
		attr := m.fileAttr(path, uint64(len(data)), mode)
		return fusekit.Result{Attr: &attr, Handle: &readOnlyFile{data: append([]byte(nil), data...)}}, nil
	case fusekit.OpCreate, fusekit.OpSetAttr, fusekit.OpUnlink, fusekit.OpMkdir, fusekit.OpRmdir, fusekit.OpRename, fusekit.OpSymlink, fusekit.OpMaterialize:
		return fusekit.Result{}, syscall.EROFS
	default:
		return fusekit.Result{}, syscall.ENOENT
	}
}

func (m *Mount) render(ctx context.Context) ([]byte, error) {
	realConfig, err := m.readSource()
	if err != nil {
		return nil, err
	}
	config := map[string]any{"$schema": "https://opencode.ai/config.json"}
	addSynthetic(config, m.projectRoot, m.instructions, m.syncedModels(ctx, realConfig))
	return marshalConfig(config)
}

func (m *Mount) readSource() (map[string]any, error) {
	result := map[string]any{}
	for _, source := range m.sourceFiles() {
		if _, err := os.Stat(source.path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		data, err := os.ReadFile(source.path)
		if err != nil {
			return nil, err
		}
		if len(bytes.TrimSpace(data)) == 0 {
			continue
		}
		var config map[string]any
		if source.format == formatYAML {
			config, err = decodeYAMLConfig(data)
		} else {
			config, err = decodeConfig(data)
		}
		if err != nil {
			return nil, err
		}
		mergeConfig(result, config)
	}
	return result, nil
}

func (m *Mount) sourceYAMLPath() string {
	return filepath.Join(m.sourceDir(), "opencode.yaml")
}

func (m *Mount) sourceFiles() []sourceFile {
	return []sourceFile{
		{path: filepath.Join(m.sourceDir(), "config.json"), format: formatJSON},
		{path: filepath.Join(m.sourceDir(), "opencode.json"), format: formatJSON},
		{path: filepath.Join(m.sourceDir(), "opencode.jsonc"), format: formatJSON},
		{path: m.sourceYAMLPath(), format: formatYAML},
	}
}

func mergeConfig(dst, src map[string]any) {
	for key, value := range src {
		srcMap, srcOK := value.(map[string]any)
		dstMap, dstOK := dst[key].(map[string]any)
		if srcOK && dstOK {
			mergeConfig(dstMap, srcMap)
			continue
		}
		dst[key] = value
	}
}

func (m *Mount) sourceDir() string {
	return m.configDir
}

func (m *Mount) attr(size uint64) fusekit.Attr {
	return m.fileAttr(ConfigPath, size, 0o400)
}

func (m *Mount) fileAttr(path string, size uint64, mode uint32) fusekit.Attr {
	attr := fusekit.Attr{
		Object: fusekit.ObjectKey{MountID: m.ID(), Kind: "file", Key: path},
		Mode:   syscall.S_IFREG | mode,
		Size:   size,
		UID:    uint32(os.Getuid()),
		GID:    uint32(os.Getgid()),
		Nlink:  1,
		ATime:  m.created,
		MTime:  m.created,
		CTime:  m.created,
	}
	return attr
}

func hasWriteFlags(flags uint32) bool {
	access := flags & syscall.O_ACCMODE
	return access == syscall.O_WRONLY || access == syscall.O_RDWR || flags&(syscall.O_TRUNC|syscall.O_APPEND|syscall.O_CREAT) != 0
}

type readOnlyFile struct {
	data []byte
}

var _ = (fusekit.FileReader)((*readOnlyFile)(nil))

func (f *readOnlyFile) Read(ctx context.Context, dest []byte, off int64) ([]byte, error) {
	if off < 0 {
		return nil, syscall.EINVAL
	}
	if int64(len(f.data)) <= off {
		return nil, nil
	}
	data := f.data[off:]
	if len(data) > len(dest) {
		data = data[:len(dest)]
	}
	return append([]byte(nil), data...), nil
}

func decodeConfig(data []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("parse opencode config: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return nil, fmt.Errorf("parse opencode config: %w", err)
		}
		return nil, errors.New("parse opencode config: multiple JSON values")
	}
	config, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("opencode config must be a JSON object")
	}
	return config, nil
}

func decodeYAMLConfig(data []byte) (map[string]any, error) {
	var value any
	if err := yaml.Unmarshal(data, &value); err != nil {
		return nil, fmt.Errorf("parse opencode config: %w", err)
	}
	if value == nil {
		return map[string]any{}, nil
	}
	normalized, err := normalizeYAML(value)
	if err != nil {
		return nil, err
	}
	config, ok := normalized.(map[string]any)
	if !ok {
		return nil, errors.New("opencode config must be a YAML object")
	}
	return config, nil
}

func normalizeYAML(value any) (any, error) {
	switch v := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(v))
		for key, item := range v {
			normalized, err := normalizeYAML(item)
			if err != nil {
				return nil, err
			}
			result[key] = normalized
		}
		return result, nil
	case map[any]any:
		result := make(map[string]any, len(v))
		for key, item := range v {
			stringKey, ok := key.(string)
			if !ok {
				return nil, fmt.Errorf("opencode config contains non-string YAML key: %v", key)
			}
			normalized, err := normalizeYAML(item)
			if err != nil {
				return nil, err
			}
			result[stringKey] = normalized
		}
		return result, nil
	case []any:
		result := make([]any, len(v))
		for i, item := range v {
			normalized, err := normalizeYAML(item)
			if err != nil {
				return nil, err
			}
			result[i] = normalized
		}
		return result, nil
	default:
		return value, nil
	}
}

func marshalConfig(config map[string]any) ([]byte, error) {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func (m *Mount) syncedModels(ctx context.Context, config map[string]any) map[string]map[string]any {
	m.modelsOnce.Do(func() {
		m.models = m.discoverModels(ctx, config)
	})
	return m.models
}

func (m *Mount) discoverModels(ctx context.Context, config map[string]any) map[string]map[string]any {
	providers, ok := config["provider"].(map[string]any)
	if !ok {
		return nil
	}
	configDir := m.sourceDir()
	models := map[string]map[string]any{}
	for providerID, rawProvider := range providers {
		provider, ok := rawProvider.(map[string]any)
		if !ok || !isOpenAICompatibleProvider(provider) {
			continue
		}
		modelIDs, err := m.fetchProviderModelIDs(ctx, providerID, provider, configDir)
		if err != nil {
			m.modelWarnings = append(m.modelWarnings, fmt.Errorf("fetch OpenCode models for provider %q: %w", providerID, err))
			continue
		}
		models[providerID] = providerModels(modelIDs)
	}
	if len(models) == 0 {
		return nil
	}
	return models
}

func isOpenAICompatibleProvider(provider map[string]any) bool {
	if provider["npm"] != "@ai-sdk/openai-compatible" {
		return false
	}
	options, ok := provider["options"].(map[string]any)
	if !ok {
		return false
	}
	baseURL, ok := options["baseURL"].(string)
	return ok && strings.TrimSpace(baseURL) != ""
}

func (m *Mount) fetchProviderModelIDs(ctx context.Context, providerID string, provider map[string]any, configDir string) ([]string, error) {
	options := provider["options"].(map[string]any)
	baseURL := strings.TrimSpace(options["baseURL"].(string))
	headers, err := resolveHeaders(providerID, options, configDir)
	if err != nil {
		return nil, err
	}
	token := ""
	if auth := headers["Authorization"]; strings.HasPrefix(auth, "Bearer ") {
		token = strings.TrimPrefix(auth, "Bearer ")
		delete(headers, "Authorization")
	}
	return openai.NewClient(m.http, baseURL, token, headers).ModelIDs(ctx)
}

func resolveHeaders(providerID string, options map[string]any, configDir string) (map[string]string, error) {
	headers := map[string]string{}
	rawHeaders := options["headers"]
	if rawHeaders == nil {
		rawHeaders = map[string]any{}
	}
	headerMap, ok := rawHeaders.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("provider %q has non-object options.headers", providerID)
	}
	for key, rawValue := range headerMap {
		value, ok := rawValue.(string)
		if !ok {
			return nil, fmt.Errorf("provider %q has non-string header values", providerID)
		}
		resolved, err := resolveString(value, configDir)
		if err != nil {
			return nil, err
		}
		headers[key] = resolved
	}
	if rawAPIKey, exists := options["apiKey"]; exists && rawAPIKey != nil {
		apiKey, ok := rawAPIKey.(string)
		if !ok {
			return nil, fmt.Errorf("provider %q has non-string options.apiKey", providerID)
		}
		resolved, err := resolveString(apiKey, configDir)
		if err != nil {
			return nil, err
		}
		if resolved != "" {
			if _, exists := headers["Authorization"]; !exists {
				headers["Authorization"] = "Bearer " + resolved
			}
		}
	}
	return headers, nil
}

func resolveString(value, configDir string) (string, error) {
	var firstErr error
	resolved := substitutionPattern.ReplaceAllStringFunc(value, func(match string) string {
		if firstErr != nil {
			return ""
		}
		parts := substitutionPattern.FindStringSubmatch(match)
		kind := parts[1]
		target := strings.TrimSpace(parts[2])
		if kind == "env" {
			return os.Getenv(target)
		}
		path := config.ExpandHome(target, homeDir())
		if !filepath.IsAbs(path) {
			path = filepath.Join(configDir, path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			firstErr = fmt.Errorf("unable to read file substitution %q: %w", target, err)
			return ""
		}
		return strings.TrimSpace(string(data))
	})
	if firstErr != nil {
		return "", firstErr
	}
	return resolved, nil
}

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

func providerModels(modelIDs []string) map[string]any {
	models := make(map[string]any, len(modelIDs))
	for _, id := range modelIDs {
		models[id] = map[string]any{"name": id}
	}
	return models
}

func addSyncedModels(config map[string]any, models map[string]map[string]any) {
	if len(models) == 0 {
		return
	}
	providers, ok := config["provider"].(map[string]any)
	if !ok {
		providers = map[string]any{}
		config["provider"] = providers
	}
	for providerID, synced := range models {
		provider, ok := providers[providerID].(map[string]any)
		if !ok {
			provider = map[string]any{}
			providers[providerID] = provider
		}
		provider["models"] = cloneModels(synced)
	}
}

func cloneModels(models map[string]any) map[string]any {
	clone := make(map[string]any, len(models))
	for id, rawModel := range models {
		if model, ok := rawModel.(map[string]any); ok {
			modelClone := make(map[string]any, len(model))
			for key, value := range model {
				modelClone[key] = value
			}
			clone[id] = modelClone
			continue
		}
		clone[id] = rawModel
	}
	return clone
}

func addSynthetic(config map[string]any, projectRoot string, instructions []string, models map[string]map[string]any) {
	mcp := objectAt(config, "mcp")
	mcp["toby"] = syntheticMCP()
	addInstructions(config, instructions)
	addSyncedModels(config, models)
	permission := objectAt(config, "permission")
	external, ok := permission["external_directory"].(map[string]any)
	if !ok {
		external = map[string]any{}
		permission["external_directory"] = external
	}
	for _, pattern := range allowedExternalDirectoryPatterns(projectRoot) {
		external[pattern] = "allow"
	}
}

func addInstructions(config map[string]any, paths []string) {
	if len(paths) == 0 {
		return
	}
	instructions, ok := config["instructions"].([]any)
	if !ok {
		instructions = []any{}
	}
	seen := map[string]bool{}
	for _, item := range instructions {
		if path, ok := item.(string); ok {
			seen[path] = true
		}
	}
	for _, path := range paths {
		if path == "" || seen[path] {
			continue
		}
		instructions = append(instructions, path)
		seen[path] = true
	}
	config["instructions"] = instructions
}

func objectAt(config map[string]any, key string) map[string]any {
	if value, ok := config[key].(map[string]any); ok {
		return value
	}
	value := map[string]any{}
	config[key] = value
	return value
}

func syntheticMCP() map[string]any {
	return map[string]any{
		"type":    "local",
		"command": []any{"toby", "mcp"},
		"enabled": true,
	}
}

func allowedExternalDirectoryPatterns(projectRoot string) []string {
	return []string{"/tmp", "/tmp/**", projectRoot, filepath.Join(projectRoot, "**")}
}
