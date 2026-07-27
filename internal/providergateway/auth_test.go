package providergateway

// Verifies that the authorization handler emits only generic allow and denial
// responses.

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthHandlerAllowsAndThenFailsClosedAfterRevoke(t *testing.T) {
	store := newRouteStore()
	item := testRoute("route-one", "cap-one", "credential-one")
	if _, err := store.add([]route{item}); err != nil {
		t.Fatal(err)
	}
	if err := store.activate([]string{item.ID}); err != nil {
		t.Fatal(err)
	}
	if err := store.setGenerationToken("generation-one"); err != nil {
		t.Fatal(err)
	}

	handler, err := newAuthHandler(store)
	if err != nil {
		t.Fatal(err)
	}

	allowed := httptest.NewRecorder()
	handler.ServeHTTP(
		allowed,
		authorizedRequest(t, item, "generation-one"),
	)
	if allowed.Code != http.StatusNoContent ||
		allowed.Body.Len() != 0 {
		t.Fatalf(
			"allowed response = %d %q",
			allowed.Code,
			allowed.Body.String(),
		)
	}

	store.revoke([]string{item.ID})
	denied := httptest.NewRecorder()
	handler.ServeHTTP(
		denied,
		authorizedRequest(t, item, "generation-one"),
	)
	if denied.Code != http.StatusNotFound ||
		denied.Body.Len() != 0 {
		t.Fatalf(
			"denied response = %d %q",
			denied.Code,
			denied.Body.String(),
		)
	}
}
