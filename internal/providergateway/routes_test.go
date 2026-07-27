package providergateway

// Proves atomic desired-state changes, deny tombstones, credential matching,
// and authorization-versus-revocation linearization.

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestRouteStoreRevocationExcludesReplayAndRetainsTombstone(t *testing.T) {
	store := newRouteStore()
	item := testRoute("route-one", "cap-one", "credential-one")

	revision, err := store.add([]route{item})
	if err != nil {
		t.Fatal(err)
	}
	if revision != 1 {
		t.Fatalf("add revision = %d, want 1", revision)
	}
	if err := store.activate([]string{item.ID}); err != nil {
		t.Fatal(err)
	}

	removalRevision := store.revoke([]string{item.ID})
	if removalRevision != 2 {
		t.Fatalf(
			"removal revision = %d, want 2",
			removalRevision,
		)
	}
	if snapshot := store.snapshot(); len(snapshot.Routes) != 0 ||
		snapshot.Revision != removalRevision {
		t.Fatalf("post-revoke snapshot = %#v", snapshot)
	}
	if _, present := store.routes[item.ID]; !present {
		t.Fatal("revoked route tombstone was removed before confirmation")
	}

	store.confirm(removalRevision - 1)
	if _, present := store.routes[item.ID]; !present {
		t.Fatal("early confirmation removed route tombstone")
	}
	store.confirm(removalRevision)
	if _, present := store.routes[item.ID]; present {
		t.Fatal("confirmed route tombstone remains")
	}
}

func TestRouteStoreRejectsMixedOrDuplicateCapabilities(t *testing.T) {
	store := newRouteStore()
	first := testRoute("route-one", "cap-one", "credential-one")
	second := testRoute("route-two", "cap-two", "credential-two")

	if _, err := store.add([]route{first, second}); err != nil {
		t.Fatal(err)
	}
	if err := store.activate([]string{first.ID, second.ID}); err != nil {
		t.Fatal(err)
	}
	if err := store.setGenerationToken("generation-one"); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		route      route
		path       string
		credential string
		capability string
		generation []string
		want       bool
	}{
		{
			name:       "valid",
			route:      first,
			path:       first.authPath(),
			credential: first.Credential,
			capability: first.Capability,
			generation: []string{"generation-one"},
			want:       true,
		},
		{
			name:       "mixed credential",
			route:      first,
			path:       first.authPath(),
			credential: second.Credential,
			capability: first.Capability,
			generation: []string{"generation-one"},
		},
		{
			name:       "mixed capability",
			route:      first,
			path:       first.authPath(),
			credential: first.Credential,
			capability: second.Capability,
			generation: []string{"generation-one"},
		},
		{
			name:       "duplicate generation",
			route:      first,
			path:       first.authPath(),
			credential: first.Credential,
			capability: first.Capability,
			generation: []string{
				"generation-one",
				"generation-one",
			},
		},
		{
			name:       "wrong method",
			route:      first,
			path:       first.authPath(),
			credential: first.Credential,
			capability: first.Capability,
			generation: []string{"generation-one"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			method := http.MethodGet
			if test.name == "wrong method" {
				method = http.MethodPost
			}
			request := httptest.NewRequest(
				method,
				"http://auth.toby.invalid"+test.path,
				nil,
			)
			for _, value := range test.generation {
				request.Header.Add(
					internalGatewayTokenHeader,
					value,
				)
			}
			request.Header.Set(
				internalCapabilityHeader,
				test.capability,
			)
			credentialHeader, _ := test.route.credentialHeader()
			prefix := test.credential
			if test.route.Provider.Type == ProviderOpenAI {
				prefix = "Bearer " + test.credential
			}
			request.Header.Set(credentialHeader, prefix)

			allowed := store.authorize(request, func() {})
			if allowed != test.want {
				t.Fatalf(
					"authorize() = %v, want %v",
					allowed,
					test.want,
				)
			}
		})
	}
}

func TestRouteStoreRevokeWaitsForAuthorizationDecision(t *testing.T) {
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

	request := authorizedRequest(t, item, "generation-one")
	allowEntered := make(chan struct{})
	releaseAllow := make(chan struct{})
	authorizationDone := make(chan bool, 1)
	go func() {
		authorizationDone <- store.authorize(request, func() {
			close(allowEntered)
			<-releaseAllow
		})
	}()

	select {
	case <-allowEntered:
	case <-time.After(time.Second):
		t.Fatal("authorization decision did not start")
	}

	revokeDone := make(chan struct{})
	go func() {
		store.revoke([]string{item.ID})
		close(revokeDone)
	}()

	select {
	case <-revokeDone:
		t.Fatal("revoke returned before the allow decision linearized")
	case <-time.After(25 * time.Millisecond):
	}

	close(releaseAllow)
	select {
	case allowed := <-authorizationDone:
		if !allowed {
			t.Fatal("pre-revoke authorization was denied")
		}
	case <-time.After(time.Second):
		t.Fatal("authorization did not finish")
	}
	select {
	case <-revokeDone:
	case <-time.After(time.Second):
		t.Fatal("revoke did not finish")
	}

	if store.authorize(
		authorizedRequest(t, item, "generation-one"),
		func() {},
	) {
		t.Fatal("authorization succeeded after revoke returned")
	}
}

func TestRouteStoreConcurrentSnapshotAndRevoke(t *testing.T) {
	store := newRouteStore()
	const count = 128

	routes := make([]route, count)
	ids := make([]string, count)
	for index := range routes {
		routes[index] = testRoute(
			"route-"+tokenSuffix(index),
			"cap-"+tokenSuffix(index),
			"credential-"+tokenSuffix(index),
		)
		ids[index] = routes[index].ID
	}
	if _, err := store.add(routes); err != nil {
		t.Fatal(err)
	}

	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		for range 100 {
			_ = store.snapshot()
		}
	}()
	go func() {
		defer wait.Done()
		store.revoke(ids)
	}()
	wait.Wait()

	if got := len(store.snapshot().Routes); got != 0 {
		t.Fatalf("desired routes after revoke = %d, want 0", got)
	}
}

func authorizedRequest(
	t *testing.T,
	item route,
	generationToken string,
) *http.Request {
	t.Helper()

	request := httptest.NewRequest(
		http.MethodGet,
		"http://auth.toby.invalid"+item.authPath(),
		nil,
	)
	request.Header.Set(
		internalGatewayTokenHeader,
		generationToken,
	)
	request.Header.Set(
		internalCapabilityHeader,
		item.Capability,
	)
	name, value := item.credentialHeader()
	request.Header.Set(name, value)

	return request
}

func testRoute(id string, capability string, credential string) route {
	return route{
		ID:         id,
		Capability: capability,
		Credential: credential,
		Provider: ProviderSpec{
			ID:   "provider-" + id,
			Type: ProviderOpenAI,
			Name: "Provider " + id,
			URL:  "https://provider.example/v1",
		},
	}
}

func tokenSuffix(index int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz"
	if index < len(alphabet) {
		return string(alphabet[index])
	}

	return string(alphabet[index/len(alphabet)-1]) +
		string(alphabet[index%len(alphabet)])
}
