package providergateway

// Renders one deterministic full Caddy native-JSON snapshot with authorization
// before upstream-secret insertion on every route.

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	caddyAdminSocket      = "/run/toby/service/admin.sock"
	caddyDataSocket       = "/run/toby/service/data.sock"
	caddyAuthSocket       = "/run/toby/auth.sock"
	caddySocketMode       = "0600"
	caddyDeleteAllHeaders = "*"
	caddyStreamCloseDelay = 5 * time.Minute
	caddyReadHeaderLimit  = 5 * time.Second
	caddyIdleLimit        = 30 * time.Second
	caddyMaxHeaderBytes   = 64 << 10
	maxCaddyConfigBytes   = 16 << 20
)

var upstreamDeletedHeaders = []string{
	"Authorization",
	"Cookie",
	"Forwarded",
	"Origin",
	"Proxy-Authorization",
	"Proxy-Connection",
	"Referer",
	"Via",
	"X-Api-Key",
	"X-Forwarded-*",
	"X-Toby-*",
}

var upstreamDeletedResponseHeaders = []string{
	"Location",
	"Server",
	"Via",
	"X-Forwarded-*",
}

func renderCaddyConfig(
	snapshot routeSnapshot,
	generationToken string,
) ([]byte, error) {
	if !validCapabilityToken(
		generationToken,
		maxCredentialBytes,
	) {
		return nil, fmt.Errorf(
			"models gateway generation token is invalid",
		)
	}

	routes := append([]route(nil), snapshot.Routes...)
	sort.Slice(routes, func(i, j int) bool {
		return routes[i].ID < routes[j].ID
	})

	renderedRoutes := make([]any, 0, len(routes)+1)
	for index, item := range routes {
		if err := item.validate(); err != nil {
			return nil, fmt.Errorf(
				"render provider route %d: %w",
				index,
				err,
			)
		}

		rendered, err := renderProviderRoute(
			item,
			generationToken,
		)
		if err != nil {
			return nil, err
		}
		renderedRoutes = append(renderedRoutes, rendered)
	}
	renderedRoutes = append(
		renderedRoutes,
		staticResponseRoute(http.StatusNotFound),
	)

	config := map[string]any{
		"admin": map[string]any{
			"listen": unixSocketAddress(
				caddyAdminSocket,
				caddySocketMode,
			),
			"config": map[string]any{
				"persist": false,
			},
		},
		"logging": map[string]any{
			"logs": map[string]any{
				"default": map[string]any{
					"writer": map[string]any{
						"output": "discard",
					},
				},
			},
		},
		"apps": map[string]any{
			"http": map[string]any{
				"servers": map[string]any{
					"providers": map[string]any{
						"listen": []string{
							unixSocketAddress(
								caddyDataSocket,
								caddySocketMode,
							),
						},
						"protocols": []string{"h1"},
						"read_header_timeout": int64(
							caddyReadHeaderLimit,
						),
						"idle_timeout": int64(
							caddyIdleLimit,
						),
						"max_header_bytes": caddyMaxHeaderBytes,
						"routes":           renderedRoutes,
						"errors":           genericErrorRoutes(),
					},
				},
			},
		},
	}

	data, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf(
			"encode provider Caddy configuration",
		)
	}
	if len(data) > maxCaddyConfigBytes {
		return nil, fmt.Errorf(
			"provider Caddy configuration exceeds %d bytes",
			maxCaddyConfigBytes,
		)
	}

	return data, nil
}

func renderProviderRoute(
	item route,
	generationToken string,
) (map[string]any, error) {
	upstream, err := url.Parse(item.Provider.URL)
	if err != nil {
		return nil, fmt.Errorf(
			"render provider route: URL is invalid",
		)
	}

	credentialHeader, credential := item.credentialHeader()
	match := map[string]any{
		"path": []string{
			item.routePrefix(),
			item.routePrefix() + "/*",
		},
		"header": map[string][]string{
			credentialHeader: {credential},
		},
	}
	handlers := []any{
		renderAuthHandler(item, generationToken),
		map[string]any{
			"handler":           "rewrite",
			"strip_path_prefix": item.routePrefix(),
		},
	}
	if basePath := upstreamBasePath(upstream); basePath != "" {
		handlers = append(handlers, map[string]any{
			"handler": "rewrite",
			"path_regexp": []any{
				map[string]any{
					"find":    "^",
					"replace": literalRegexpReplacement(basePath),
				},
			},
		})
	}

	proxy, err := renderUpstreamProxy(item, upstream)
	if err != nil {
		return nil, err
	}
	handlers = append(handlers, proxy)

	return map[string]any{
		"match":  []any{match},
		"handle": handlers,
	}, nil
}

func renderAuthHandler(
	item route,
	generationToken string,
) map[string]any {
	credentialHeader, credential := item.credentialHeader()
	headers := map[string][]string{
		internalCapabilityHeader: {
			item.Capability,
		},
		internalGatewayTokenHeader: {
			generationToken,
		},
		credentialHeader: {
			credential,
		},
	}

	return map[string]any{
		"handler": "reverse_proxy",
		"rewrite": map[string]any{
			"method": http.MethodGet,
			"uri":    item.authPath() + "?",
		},
		"headers": map[string]any{
			"request": map[string]any{
				"delete": []string{caddyDeleteAllHeaders},
				"set":    headers,
			},
		},
		"upstreams": []any{
			map[string]any{
				"dial": unixSocketAddress(
					caddyAuthSocket,
					"",
				),
			},
		},
		"handle_response": []any{
			map[string]any{
				"match": map[string]any{
					"status_code": []int{2},
				},
				"routes": []any{
					map[string]any{
						"handle": []any{
							map[string]any{
								"handler": "vars",
							},
						},
					},
				},
			},
		},
	}
}

func renderUpstreamProxy(
	item route,
	upstream *url.URL,
) (map[string]any, error) {
	address, err := upstreamDialAddress(upstream)
	if err != nil {
		return nil, fmt.Errorf(
			"render provider %q upstream",
			item.Provider.ID,
		)
	}

	deleted := append(
		[]string(nil),
		upstreamDeletedHeaders...,
	)
	set := make(map[string][]string, len(item.Provider.Headers)+1)
	for name, value := range item.Provider.Headers {
		canonical := http.CanonicalHeaderKey(name)
		set[canonical] = []string{value}
		deleted = removeHeaderName(deleted, canonical)
	}
	set["Host"] = []string{upstream.Host}
	sort.Strings(deleted)

	handler := map[string]any{
		"handler": "reverse_proxy",
		"upstreams": []any{
			map[string]any{
				"dial": address,
			},
		},
		"headers": map[string]any{
			"request": map[string]any{
				"delete": deleted,
				"set":    set,
			},
			"response": map[string]any{
				"delete": append(
					[]string(nil),
					upstreamDeletedResponseHeaders...,
				),
			},
		},
		"stream_close_delay": int64(caddyStreamCloseDelay),
		"handle_response": []any{
			map[string]any{
				"match": map[string]any{
					"status_code": []int{3},
				},
				"routes": []any{
					staticResponseRoute(
						http.StatusBadGateway,
					),
				},
			},
		},
	}
	if upstream.Scheme == "https" {
		handler["transport"] = map[string]any{
			"protocol": "http",
			"tls": map[string]any{
				"server_name": upstream.Hostname(),
			},
		}
	}

	return handler, nil
}

func upstreamDialAddress(upstream *url.URL) (string, error) {
	if upstream == nil || upstream.Hostname() == "" {
		return "", fmt.Errorf("provider upstream host is missing")
	}

	port := upstream.Port()
	if port == "" {
		switch upstream.Scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		default:
			return "", fmt.Errorf(
				"provider upstream scheme is unsupported",
			)
		}
	}

	return net.JoinHostPort(upstream.Hostname(), port), nil
}

func upstreamBasePath(upstream *url.URL) string {
	if upstream == nil {
		return ""
	}

	base := strings.TrimRight(upstream.EscapedPath(), "/")
	if base == "" {
		return ""
	}

	return base
}

func literalRegexpReplacement(value string) string {
	return strings.ReplaceAll(value, "$", "$$")
}

func removeHeaderName(
	names []string,
	removed string,
) []string {
	result := names[:0]
	for _, name := range names {
		if strings.EqualFold(name, removed) {
			continue
		}
		result = append(result, name)
	}

	return result
}

func unixSocketAddress(path string, mode string) string {
	address := "unix/" + path
	if mode != "" {
		address += "|" + mode
	}

	return address
}

func staticResponseRoute(status int) map[string]any {
	return map[string]any{
		"handle": []any{
			map[string]any{
				"handler":     "static_response",
				"status_code": status,
			},
		},
	}
}

func genericErrorRoutes() map[string]any {
	return map[string]any{
		"routes": []any{
			map[string]any{
				"handle": []any{
					map[string]any{
						"handler": "subroute",
						"routes": []any{
							staticResponseRoute(
								http.StatusBadGateway,
							),
						},
					},
				},
			},
		},
	}
}
