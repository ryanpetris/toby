package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/openai"
	"petris.dev/toby/internal/tool"

	"github.com/spf13/cobra"
)

func init() { register(newOpenCodeTool) }

var substitutionPattern = regexp.MustCompile(`\{(env|file):([^}]+)\}`)

type openCodeTool struct {
	tool.Base
	paths config.Paths
	http  *http.Client
}

func newOpenCodeTool(paths config.Paths, client *http.Client) tool.Tool {
	return &openCodeTool{
		Base:  tool.Base{Metadata: tool.Metadata{Name: tool.OpenCodeToolName, LaunchHelp: "Launch OpenCode", Dependencies: []string{tool.NpmToolName}, ContextGroups: []string{tool.GroupAI, tool.GroupSystem, tool.GroupVCS}}},
		paths: paths,
		http:  client,
	}
}

func (t *openCodeTool) ConfigureCommand(cmd *cobra.Command) {
	cmd.Flags().Bool("sync-models", false, "Sync configured model lists in the OpenCode config before launching.")
}

func (t *openCodeTool) HostInit(ctx context.Context, opts *tool.CommandOptions) error {
	if err := os.MkdirAll(filepath.Join(t.paths.SandboxRoot, ".config", "opencode"), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(t.paths.SandboxRoot, ".config", "opencode-share"), 0o755); err != nil {
		return err
	}
	if !opts.SyncModels {
		return nil
	}
	return t.syncModels(ctx)
}

func (t *openCodeTool) Binds() []tool.Bind {
	return []tool.Bind{
		{HostPath: filepath.Join(t.paths.SandboxRoot, ".config", "opencode"), SandboxPath: filepath.Join(t.paths.Home, ".config", "opencode"), Type: tool.BindRegular},
		{HostPath: filepath.Join(t.paths.SandboxRoot, ".config", "opencode-share"), SandboxPath: filepath.Join(t.paths.Home, ".local", "share", "opencode"), Type: tool.BindRegular},
	}
}

func (t *openCodeTool) configPath() string {
	return filepath.Join(t.paths.SandboxRoot, ".config", "opencode", "opencode.json")
}

func (t *openCodeTool) Install(ctx context.Context, run *tool.RunContext, force bool) error {
	if !force {
		exists, err := tool.CommandExists(ctx, run, "opencode")
		if err != nil || exists {
			return err
		}
	}
	return tool.RunCommand(ctx, run.Exec, []string{"npm", "install", "-g", "opencode-ai"}, tool.ExecOptions{})
}

func (t *openCodeTool) Launch(ctx context.Context, run *tool.RunContext) error {
	return tool.RunCommand(ctx, run.Launch, append([]string{"opencode"}, run.Extra...), tool.ExecOptions{})
}

func (t *openCodeTool) syncModels(ctx context.Context) error {
	path := t.configPath()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	configData, err := loadJSONObject(path)
	if err != nil {
		return fmt.Errorf("failed to load OpenCode config: %w", err)
	}
	providers, ok := configData["provider"].(map[string]any)
	if !ok {
		return fmt.Errorf("failed to sync OpenCode models: no provider object found in %s", path)
	}
	configDir := filepath.Dir(path)
	anyChanged := false
	successCount := 0
	failureCount := 0
	for providerID, rawProvider := range providers {
		provider, ok := rawProvider.(map[string]any)
		if !ok || !isOpenAICompatibleProvider(provider) {
			continue
		}
		modelIDs, err := t.fetchProviderModelIDs(ctx, providerID, provider, configDir)
		if err != nil {
			failureCount++
			log.Printf("failed to sync provider %q: %s", providerID, err)
			continue
		}
		successCount++
		anyChanged = syncProviderModels(provider, modelIDs) || anyChanged
	}
	if successCount == 0 {
		if failureCount > 0 {
			log.Printf("no OpenCode providers synced successfully")
		}
		return nil
	}
	if anyChanged {
		return writeJSONObject(path, configData)
	}
	return nil
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

func (t *openCodeTool) fetchProviderModelIDs(ctx context.Context, providerID string, provider map[string]any, configDir string) ([]string, error) {
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
	return openai.NewClient(t.http, baseURL, token, headers).ModelIDs(ctx)
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

func syncProviderModels(provider map[string]any, modelIDs []string) bool {
	models := make(map[string]any, len(modelIDs))
	for _, id := range modelIDs {
		models[id] = map[string]any{"name": id}
	}
	if mapsEqual(provider["models"], models) {
		return false
	}
	provider["models"] = models
	return true
}

func mapsEqual(a any, b map[string]any) bool {
	aJSON, err := json.Marshal(a)
	if err != nil {
		return false
	}
	bJSON, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return bytes.Equal(aJSON, bJSON)
}

func loadJSONObject(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	if result == nil {
		return nil, fmt.Errorf("expected top-level JSON object in %s", path)
	}
	return result, nil
}

func writeJSONObject(path string, data map[string]any) error {
	payload, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return os.WriteFile(path, payload, 0o644)
}
