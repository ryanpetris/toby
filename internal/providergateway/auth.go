package providergateway

// Serves fail-closed per-request authorization for Caddy over a protected Unix
// socket without handling provider request or response bodies.

import (
	"net/http"
)

type authHandler struct {
	routes *routeStore
}

var _ http.Handler = (*authHandler)(nil)

func newAuthHandler(routes *routeStore) (*authHandler, error) {
	if routes == nil {
		return nil, errRouteStoreRequired
	}

	return &authHandler{routes: routes}, nil
}

func (h *authHandler) ServeHTTP(
	writer http.ResponseWriter,
	request *http.Request,
) {
	if h == nil || h.routes == nil {
		writeAuthorizationDenied(writer)
		return
	}

	allowed := h.routes.authorize(request, func() {
		writer.WriteHeader(http.StatusNoContent)
	})
	if !allowed {
		writeAuthorizationDenied(writer)
	}
}

func writeAuthorizationDenied(writer http.ResponseWriter) {
	writer.Header().Set("Content-Length", "0")
	writer.WriteHeader(http.StatusNotFound)
}
