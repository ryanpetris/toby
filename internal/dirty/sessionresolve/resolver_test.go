package sessionresolve

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tobyconfig "petris.dev/toby/config/toby"
	"petris.dev/toby/container/engine"
	contextfiles "petris.dev/toby/context/files"
	"petris.dev/toby/control"
	"petris.dev/toby/control/httpproxy"
	"petris.dev/toby/internal/dirty/control/mcpproxy"
	"petris.dev/toby/lifecycle"
	"petris.dev/toby/providers"
	"petris.dev/toby/providers/openai"
	"petris.dev/toby/sessionconfig"
	"petris.dev/toby/tools"
	"petris.dev/toby/tools/fake"
)

const (
	testControlHost = "127.0.0.1:12345"
	testTobyMCPURL  = "http://127.0.0.1:12345/proxy/toby"
)

func loadConfig(t *testing.T, body string) *tobyconfig.Service {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := tobyconfig.Load(dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func newSandbox() *fake.Sandbox {
	sandbox := fake.NewSandbox("")
	sandbox.Env[control.EnvControlHost] = testControlHost
	sandbox.MCPURL = testTobyMCPURL
	return sandbox
}

func resolve(t *testing.T, p Params, stderr *bytes.Buffer) sessionconfig.Config {
	t.Helper()
	if p.Holder == nil {
		p.Holder = sessionconfig.NewHolder()
	}
	hooks := NewLifecycleHooks(p)
	lctx := lifecycle.Context{Options: &tools.Options{}, Stderr: stderr}
	if err := hooks.Hook.Run(context.Background(), lctx); err != nil {
		t.Fatal(err)
	}
	return p.Holder.Get()
}

func TestResolveMCPServersIncludesTobyAndProxiesConfigured(t *testing.T) {
	cfg := loadConfig(t, `{"mcp":{"server":{"docs":{"type":"remote","url":"https://example.com/mcp"}}}}`)
	proxy := httpproxy.NewService(nil)
	mcpProxy, err := mcpproxy.NewService(mcpproxy.ServiceParams{Proxy: proxy, Runtimes: []mcpproxy.Runtime{mcpproxy.NewDockerRunner(engine.New())}})
	if err != nil {
		t.Fatal(err)
	}
	if err := mcpProxy.Configure(context.Background(), testControlHost, cfg, mcpproxy.Defaults{}); err != nil {
		t.Fatal(err)
	}
	config := resolve(t, Params{Config: cfg, MCPProxy: mcpProxy, Proxy: proxy, Providers: emptyRegistry(), ContextFiles: contextfiles.NewService(), Sandbox: newSandbox()}, nil)

	byName := map[string]string{}
	for _, server := range config.MCPServers {
		byName[server.Name] = server.URL
	}
	if byName["toby"] != testTobyMCPURL {
		t.Fatalf("toby server = %#v", config.MCPServers)
	}
	if url := byName["docs"]; !strings.HasPrefix(url, "http://"+testControlHost+"/proxy/") {
		t.Fatalf("docs server url = %q", url)
	}
}

func TestResolveProvidersFetchesModelsAndProxies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"alpha"},{"id":"beta"}]}`))
	}))
	t.Cleanup(server.Close)
	cfg := loadConfig(t, `{"provider":{"local":{"type":"openai","baseURL":"`+server.URL+`"}}}`)
	registry := providers.NewRegistry([]providers.Client{openai.New(server.Client())})

	config := resolve(t, Params{Config: cfg, Proxy: httpproxy.NewService(nil), Providers: registry, ContextFiles: contextfiles.NewService(), Sandbox: newSandbox()}, nil)

	if len(config.Providers) != 1 {
		t.Fatalf("providers = %#v", config.Providers)
	}
	provider := config.Providers[0]
	if provider.ID != "local" || !strings.HasPrefix(provider.BaseURL, "http://"+testControlHost+"/proxy/") {
		t.Fatalf("provider = %#v", provider)
	}
	if _, ok := provider.Models["alpha"]; !ok {
		t.Fatalf("models = %#v", provider.Models)
	}
}

func TestResolveProvidersModelFetchFailureWarnsAndOmits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	cfg := loadConfig(t, `{"provider":{"local":{"type":"openai","baseURL":"`+server.URL+`"}}}`)
	registry := providers.NewRegistry([]providers.Client{openai.New(server.Client())})

	var stderr bytes.Buffer
	config := resolve(t, Params{Config: cfg, Proxy: httpproxy.NewService(nil), Providers: registry, ContextFiles: contextfiles.NewService(), Sandbox: newSandbox()}, &stderr)

	if len(config.Providers) != 0 {
		t.Fatalf("failed provider should be omitted: %#v", config.Providers)
	}
	if !strings.Contains(stderr.String(), "warning[provider.model-discovery]") {
		t.Fatalf("warning = %q", stderr.String())
	}
}

func emptyRegistry() *providers.Registry {
	return providers.NewRegistry(nil)
}
