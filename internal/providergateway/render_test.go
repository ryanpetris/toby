package providergateway

// Verifies deterministic Caddy JSON structure, authorization ordering, header
// replacement, base-path rewriting, TLS identity, and generic failures.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestRenderCaddyConfigOrdersRoutesAndAuthenticatesBeforeSecrets(t *testing.T) {
	openAI := testRoute(
		"route-zeta",
		"cap-zeta",
		"fake-zeta",
	)
	openAI.Provider.ID = "openai"
	openAI.Provider.URL = "https://provider.example/v1"
	openAI.Provider.Headers = map[string]string{
		"Authorization": "Bearer real-openai-secret",
		"X-Provider":    "fixed",
	}
	anthropic := testRoute(
		"route-alpha",
		"cap-alpha",
		"fake-alpha",
	)
	anthropic.Provider.ID = "anthropic"
	anthropic.Provider.Type = ProviderAnthropic
	anthropic.Provider.URL = "http://127.0.0.2:8080/base"
	anthropic.Provider.Headers = map[string]string{
		"X-Api-Key": "real-anthropic-secret",
	}

	first, err := renderCaddyConfig(routeSnapshot{
		Revision: 7,
		Routes:   []route{openAI, anthropic},
	}, "generation-token")
	if err != nil {
		t.Fatal(err)
	}
	second, err := renderCaddyConfig(routeSnapshot{
		Revision: 7,
		Routes:   []route{anthropic, openAI},
	}, "generation-token")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("Caddy rendering depends on input route order")
	}

	var document map[string]any
	if err := json.Unmarshal(first, &document); err != nil {
		t.Fatal(err)
	}
	server := renderedServer(t, document)
	listeners := server["listen"].([]any)
	if got := listeners[0]; got !=
		"unix//run/toby/service/data.sock|0600" {
		t.Fatalf("data listener = %q", got)
	}
	if got := stringSlice(t, server["protocols"]); !reflect.DeepEqual(
		got,
		[]string{"h1"},
	) {
		t.Fatalf("data protocols = %#v, want h1 only", got)
	}
	if got := server["read_header_timeout"]; got !=
		float64(caddyReadHeaderLimit) {
		t.Fatalf("read-header timeout = %#v", got)
	}
	if got := server["idle_timeout"]; got !=
		float64(caddyIdleLimit) {
		t.Fatalf("idle timeout = %#v", got)
	}
	if got := server["max_header_bytes"]; got !=
		float64(caddyMaxHeaderBytes) {
		t.Fatalf("max header bytes = %#v", got)
	}
	routes := server["routes"].([]any)
	if len(routes) != 3 {
		t.Fatalf("rendered route count = %d, want 3", len(routes))
	}

	firstRoute := routes[0].(map[string]any)
	match := firstRoute["match"].([]any)[0].(map[string]any)
	paths := match["path"].([]any)
	if paths[0] != "/cap-alpha/anthropic" {
		t.Fatalf("first route path = %q", paths[0])
	}
	handlers := firstRoute["handle"].([]any)
	if got := handlers[0].(map[string]any)["handler"]; got !=
		"reverse_proxy" {
		t.Fatalf("first handler = %q, want auth reverse_proxy", got)
	}
	if got := handlers[1].(map[string]any)["strip_path_prefix"]; got !=
		"/cap-alpha/anthropic" {
		t.Fatalf("strip prefix = %q", got)
	}
	baseRewrite := handlers[2].(map[string]any)
	baseReplacements := baseRewrite["path_regexp"].([]any)
	baseReplacement := baseReplacements[0].(map[string]any)
	if got := baseReplacement["find"]; got != "^" {
		t.Fatalf("base rewrite pattern = %q", got)
	}
	if got := baseReplacement["replace"]; got != "/base" {
		t.Fatalf("base rewrite replacement = %q", got)
	}

	auth := handlers[0].(map[string]any)
	authRewrite := auth["rewrite"].(map[string]any)
	if got := authRewrite["uri"]; got != "/authorize/route-alpha?" {
		t.Fatalf("auth rewrite URI = %q, want explicit query clearing", got)
	}
	authUpstreams := auth["upstreams"].([]any)
	if got := authUpstreams[0].(map[string]any)["dial"]; got !=
		"unix//run/toby/auth.sock" {
		t.Fatalf("auth upstream = %q", got)
	}
	authRequestHeaders := auth["headers"].(map[string]any)["request"].(map[string]any)
	if got := stringSlice(
		t,
		authRequestHeaders["delete"],
	); !reflect.DeepEqual(got, []string{"*"}) {
		t.Fatalf("auth deleted headers = %#v, want delete-all", got)
	}
	authSetHeaders := authRequestHeaders["set"].(map[string]any)
	if got := stringSlice(
		t,
		authSetHeaders[anthropicCredentialHeader],
	); !reflect.DeepEqual(got, []string{"fake-alpha"}) {
		t.Fatalf("auth synthetic credential = %#v", got)
	}
	if got := stringSlice(
		t,
		authSetHeaders[internalCapabilityHeader],
	); !reflect.DeepEqual(got, []string{"cap-alpha"}) {
		t.Fatalf("auth capability = %#v", got)
	}
	if got := stringSlice(
		t,
		authSetHeaders[internalGatewayTokenHeader],
	); !reflect.DeepEqual(got, []string{"generation-token"}) {
		t.Fatalf("auth generation token = %#v", got)
	}
	authJSON, err := json.Marshal(auth)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		"real-anthropic-secret",
		"real-openai-secret",
		"provider.example",
	} {
		if bytes.Contains(authJSON, []byte(secret)) {
			t.Fatalf("auth handler contains protected upstream value")
		}
	}

	proxy := handlers[3].(map[string]any)
	requestHeaders := proxy["headers"].(map[string]any)["request"].(map[string]any)
	deleted := stringSlice(t, requestHeaders["delete"])
	if containsFold(deleted, "X-Api-Key") {
		t.Fatal("configured X-Api-Key is both deleted and set")
	}
	set := requestHeaders["set"].(map[string]any)
	if got := stringSlice(t, set["X-Api-Key"]); !reflect.DeepEqual(
		got,
		[]string{"real-anthropic-secret"},
	) {
		t.Fatalf("upstream key = %#v", got)
	}
	if got := stringSlice(t, set["Host"]); !reflect.DeepEqual(
		got,
		[]string{"127.0.0.2:8080"},
	) {
		t.Fatalf("upstream Host = %#v", got)
	}
	if _, present := proxy["transport"]; present {
		t.Fatal("plain HTTP upstream received TLS transport")
	}

	openAIRoute := routes[1].(map[string]any)
	openAIHandlers := openAIRoute["handle"].([]any)
	openAIAuth := openAIHandlers[0].(map[string]any)
	openAIAuthSet := openAIAuth["headers"].(map[string]any)["request"].(map[string]any)["set"].(map[string]any)
	if got := stringSlice(
		t,
		openAIAuthSet[openAICredentialHeader],
	); !reflect.DeepEqual(got, []string{"Bearer fake-zeta"}) {
		t.Fatalf("OpenAI auth synthetic credential = %#v", got)
	}
	openAIProxy := openAIHandlers[3].(map[string]any)
	transport := openAIProxy["transport"].(map[string]any)
	tls := transport["tls"].(map[string]any)
	if tls["server_name"] != "provider.example" {
		t.Fatalf("TLS server name = %q", tls["server_name"])
	}

	fallback := routes[2].(map[string]any)
	fallbackHandler := fallback["handle"].([]any)[0].(map[string]any)
	if fallbackHandler["status_code"] != float64(http.StatusNotFound) {
		t.Fatalf(
			"fallback status = %#v",
			fallbackHandler["status_code"],
		)
	}
}

func TestLiteralRegexpReplacementEscapesExpansionSyntax(t *testing.T) {
	if got := literalRegexpReplacement(
		"/base/$1/${name}/plain",
	); got != "/base/$$1/$${name}/plain" {
		t.Fatalf("literal replacement = %q", got)
	}
}

func TestRenderCaddyConfigWithholdsRedirectsAndDisablesPersistence(t *testing.T) {
	item := testRoute(
		"route-one",
		"cap-one",
		"fake-one",
	)
	data, err := renderCaddyConfig(
		routeSnapshot{Routes: []route{item}},
		"generation-token",
	)
	if err != nil {
		t.Fatal(err)
	}

	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	admin := document["admin"].(map[string]any)
	if admin["listen"] !=
		"unix//run/toby/service/admin.sock|0600" {
		t.Fatalf("admin listener = %q", admin["listen"])
	}
	if persist := admin["config"].(map[string]any)["persist"]; persist != false {
		t.Fatalf("admin persist = %#v", persist)
	}
	writer := document["logging"].(map[string]any)["logs"].(map[string]any)["default"].(map[string]any)["writer"].(map[string]any)
	if writer["output"] != "discard" {
		t.Fatalf("Caddy log output = %q", writer["output"])
	}

	handlers := renderedServer(t, document)["routes"].([]any)[0].(map[string]any)["handle"].([]any)
	proxy := handlers[len(handlers)-1].(map[string]any)
	responses := proxy["handle_response"].([]any)
	redirect := responses[0].(map[string]any)
	if got := redirect["match"].(map[string]any)["status_code"].([]any)[0]; got != float64(3) {
		t.Fatalf("redirect matcher = %#v", got)
	}
	responseDeleted := proxy["headers"].(map[string]any)["response"].(map[string]any)["delete"]
	if !containsFold(stringSlice(t, responseDeleted), "Location") {
		t.Fatal("upstream Location response header is not removed")
	}
}

func renderedServer(
	t *testing.T,
	document map[string]any,
) map[string]any {
	t.Helper()

	return document["apps"].(map[string]any)["http"].(map[string]any)["servers"].(map[string]any)["providers"].(map[string]any)
}

func stringSlice(t *testing.T, value any) []string {
	t.Helper()

	raw, ok := value.([]any)
	if !ok {
		t.Fatalf("value has type %T, want []any", value)
	}
	result := make([]string, len(raw))
	for index, item := range raw {
		text, ok := item.(string)
		if !ok {
			t.Fatalf("slice item has type %T, want string", item)
		}
		result[index] = text
	}

	return result
}

func containsFold(values []string, expected string) bool {
	for _, value := range values {
		if strings.EqualFold(value, expected) {
			return true
		}
	}

	return false
}
