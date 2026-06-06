// Package resolve produces the sandbox-safe sessionconfig.Config for a
// launch. It is the single privileged place that turns the raw host config, the
// MCP proxy, the HTTP proxy, and the provider registry into proxied URLs and
// non-secret data: it reads each enabled MCP server's proxied URL, registers
// each provider's upstream behind the proxy and fetches its models, and gathers
// the rendered instruction files. It runs as a context-files lifecycle hook
// (after instructions are registered, before tools render) and stores the
// result in a sessionconfig.Holder the tools read. Nothing here is gated to a
// particular tool — the resolved config is tool-agnostic.
package resolve

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"go.uber.org/fx"

	configfile "petris.dev/toby/config/file"
	"petris.dev/toby/config/session"
	contextfiles "petris.dev/toby/context/files"
	"petris.dev/toby/diagnostic/warning"
	appconfig "petris.dev/toby/internal/config/app"
	"petris.dev/toby/internal/control/httpproxy"
	"petris.dev/toby/internal/control/mcpproxy"
	"petris.dev/toby/internal/lifecycle"
	"petris.dev/toby/providers"
	"petris.dev/toby/sandbox"
)

// tobyServerName is the reserved name of Toby's own built-in MCP server.
const tobyServerName = "toby"

// Params are the resolver's injected dependencies.
type Params struct {
	fx.In

	Holder       *sessionconfig.Holder
	Config       *appconfig.Service
	MCPProxy     *mcpproxy.Service  `optional:"true"`
	Proxy        *httpproxy.Service `optional:"true"`
	Providers    *providers.Registry
	ContextFiles *contextfiles.Service
	Sandbox      sandbox.Service
}

// Resolver builds the resolved session config from the privileged inputs.
type Resolver struct {
	holder       *sessionconfig.Holder
	config       *appconfig.Service
	mcpProxy     *mcpproxy.Service
	proxy        *httpproxy.Service
	providers    *providers.Registry
	contextFiles *contextfiles.Service
	sandbox      sandbox.Service
}

// HooksResult registers the resolve hook into the lifecycle group.
type HooksResult struct {
	fx.Out

	Hook lifecycle.Hook `group:"lifecycle"`
}

// NewLifecycleHooks builds the resolver and registers it as a context-files
// hook. Priority -50 places it after the instruction-registering hooks
// (-200/-100) and before the tools (positive priority), so instructions are
// available and the resolved config is ready when tools render.
func NewLifecycleHooks(p Params) HooksResult {
	r := &Resolver{
		holder:       p.Holder,
		config:       p.Config,
		mcpProxy:     p.MCPProxy,
		proxy:        p.Proxy,
		providers:    p.Providers,
		contextFiles: p.ContextFiles,
		sandbox:      p.Sandbox,
	}
	return HooksResult{Hook: lifecycle.Hook{
		Phase:    lifecycle.PhaseContextFiles,
		Name:     "context.session-config",
		Priority: -50,
		Run:      r.run,
	}}
}

func (r *Resolver) run(ctx context.Context, lctx lifecycle.Context) error {
	cfg, err := r.resolve(ctx, lctx)
	if err != nil {
		return err
	}
	r.holder.Set(cfg)
	return nil
}

func (r *Resolver) resolve(ctx context.Context, lctx lifecycle.Context) (sessionconfig.Config, error) {
	mcpServers, err := r.resolveMCP()
	if err != nil {
		return sessionconfig.Config{}, err
	}
	resolvedProviders, err := r.resolveProviders(ctx, lctx)
	if err != nil {
		return sessionconfig.Config{}, err
	}
	return sessionconfig.Config{
		MCPServers:  mcpServers,
		Providers:   resolvedProviders,
		Permissions: r.config.PermissionPaths(),
		Instructions: sessionconfig.Instructions{
			Paths:    r.contextFiles.InstructionPaths(),
			Contents: r.contextFiles.InstructionContents(),
		},
	}, nil
}

// resolveMCP returns the proxied URL for every enabled MCP server plus Toby's
// built-in server. Each server is already registered with the proxy by
// mcpproxy.Configure, so this is a lookup; disabled servers are omitted.
func (r *Resolver) resolveMCP() ([]sessionconfig.MCPServer, error) {
	var servers []sessionconfig.MCPServer
	configured := r.config.MCPServers()
	names := make([]string, 0, len(configured))
	for name, server := range configured {
		if name == tobyServerName || !server.Enabled() {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		url, ok := r.mcpProxy.URL(name)
		if !ok || strings.TrimSpace(url) == "" {
			return nil, fmt.Errorf("mcp.%s: no proxied url (mcp proxy not configured)", name)
		}
		servers = append(servers, sessionconfig.MCPServer{Name: name, URL: url})
	}
	tobyURL := strings.TrimSpace(r.sandbox.TobyMCPURL())
	if tobyURL == "" {
		return nil, fmt.Errorf("toby MCP proxy URL is required")
	}
	servers = append(servers, sessionconfig.MCPServer{Name: tobyServerName, URL: tobyURL})
	return servers, nil
}

// resolveProviders registers each configured provider's upstream behind the
// proxy and resolves its models. Provider credentials (resolved headers) and the
// real base URL stay on the host; tools receive only the proxied URL. A model
// fetch failure warns and omits that provider, matching prior behavior.
func (r *Resolver) resolveProviders(ctx context.Context, lctx lifecycle.Context) ([]sessionconfig.Provider, error) {
	configured := r.config.Providers()
	if len(configured) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(configured))
	for id := range configured {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out []sessionconfig.Provider
	for _, id := range ids {
		provider := configured[id]
		if provider.Type != appconfig.ProviderTypeAnthropic && provider.Type != appconfig.ProviderTypeOpenAI {
			continue
		}
		if r.proxy == nil {
			return nil, fmt.Errorf("provider %q requires http proxy service", id)
		}
		headers, err := r.config.ResolveProviderHeaders(id, provider)
		if err != nil {
			return nil, err
		}
		proxyID, err := r.proxy.RegisterUpstream(provider.URL, headers)
		if err != nil {
			return nil, fmt.Errorf("register provider %q proxy: %w", id, err)
		}
		resolved := sessionconfig.Provider{
			ID:   id,
			Type: provider.Type,
			Name: provider.Name,
			URL:  r.sandbox.ProxyBaseURL(proxyID),
		}
		if provider.HasModels() {
			resolved.Models = configfile.CloneMap(provider.Models)
		} else {
			models, err := r.fetchModels(ctx, provider, headers)
			if err != nil {
				warning.Fprintf(lctx.Stderr, suppression(lctx), warning.ModelDiscovery, "failed to fetch models for provider %q: %v", id, err)
				continue
			}
			resolved.Models = models
		}
		out = append(out, resolved)
	}
	return out, nil
}

func (r *Resolver) fetchModels(ctx context.Context, provider appconfig.ProviderConfig, headers http.Header) (map[string]any, error) {
	kind := providers.KindOpenAI
	if provider.Type == appconfig.ProviderTypeAnthropic {
		kind = providers.KindAnthropic
	}
	models, err := r.providers.LookupModels(ctx, kind, provider.URL, headerStrings(headers))
	if err != nil {
		return nil, err
	}
	return providerModels(models), nil
}

func suppression(lctx lifecycle.Context) warning.Suppression {
	return lctx.SuppressWarnings
}

func headerStrings(headers http.Header) map[string]string {
	items := make(map[string]string, len(headers))
	for key, values := range headers {
		if len(values) == 0 {
			continue
		}
		items[key] = strings.Join(values, ",")
	}
	return items
}

func providerModels(modelList []providers.Model) map[string]any {
	models := make(map[string]any, len(modelList))
	for _, model := range modelList {
		models[model.ID] = map[string]any{"name": model.DisplayName}
	}
	return models
}
