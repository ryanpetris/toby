package resourcelease

// Verifies stable resource deduplication and independent lease authority.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"petris.dev/toby/internal/agent/protocol"
	agentserver "petris.dev/toby/internal/agent/server"
	"petris.dev/toby/internal/resourcehash"
)

func TestCanonicalAcquireSharesResourceWithIndependentLeases(t *testing.T) {
	service := newTestService(t)

	first := acquireTestResource(
		t,
		service,
		testMCPConfiguration(
			"https://example.invalid",
			`{"b":"2","a":"1"}`,
		),
	)
	second := acquireTestResource(
		t,
		service,
		testMCPConfiguration(
			"https://example.invalid",
			`{"a":"1","b":"2"}`,
		),
	)

	if first.ResourceID() != second.ResourceID() {
		t.Fatalf(
			"equivalent configurations produced %q and %q",
			first.ResourceID(),
			second.ResourceID(),
		)
	}
	if first.LeaseID() == second.LeaseID() {
		t.Fatalf("independent acquisitions reused lease %q", first.LeaseID())
	}
	if got := service.Snapshot(); got != (Snapshot{
		ActiveResources: 1,
		ActiveLeases:    2,
	}) {
		t.Fatalf("snapshot = %#v", got)
	}

	if err := first.Release(t.Context()); err != nil {
		t.Fatalf("release first lease: %v", err)
	}
	if got := service.Snapshot(); got != (Snapshot{
		ActiveResources: 1,
		ActiveLeases:    1,
	}) {
		t.Fatalf("snapshot after first release = %#v", got)
	}

	if err := second.Release(t.Context()); err != nil {
		t.Fatalf("release second lease: %v", err)
	}
	if got := service.Snapshot(); got != (Snapshot{}) {
		t.Fatalf("snapshot after final release = %#v", got)
	}
}

func TestAcquireSeparatesDifferentConfigurations(t *testing.T) {
	service := newTestService(t)

	first := acquireTestResource(
		t,
		service,
		testMCPConfiguration("https://one.invalid", `{}`),
	)
	second := acquireTestResource(
		t,
		service,
		testMCPConfiguration("https://two.invalid", `{}`),
	)

	if first.ResourceID() == second.ResourceID() {
		t.Fatalf("different configurations shared %q", first.ResourceID())
	}
	if got := service.Snapshot(); got != (Snapshot{
		ActiveResources: 2,
		ActiveLeases:    2,
	}) {
		t.Fatalf("snapshot = %#v", got)
	}
}

func TestAcquireCanonicalizesEmptyOptionalCollections(t *testing.T) {
	service := newTestService(t)

	withoutHeaders := acquireTestResource(
		t,
		service,
		`{"type":"configured","server":{"type":"remote",`+
			`"transport":"http","url":"https://example.invalid"}}`,
	)
	withEmptyHeaders := acquireTestResource(
		t,
		service,
		testMCPConfiguration("https://example.invalid", `{}`),
	)

	if withoutHeaders.ResourceID() != withEmptyHeaders.ResourceID() {
		t.Fatalf(
			"empty optional headers changed identity from %q to %q",
			withoutHeaders.ResourceID(),
			withEmptyHeaders.ResourceID(),
		)
	}
}

func TestAcquireRejectsUnconfiguredKind(t *testing.T) {
	service := newTestService(t)

	_, err := service.AcquireResource(
		t.Context(),
		protocol.ResourceAcquireRequest{
			Kind:          protocol.ResourceModels,
			Configuration: json.RawMessage(`{}`),
		},
		nil,
	)
	if err == nil {
		t.Fatal("acquire unconfigured resource succeeded")
	}
}

func TestAcquireDetectsIdentityCollision(t *testing.T) {
	resolver := &changingResolver{
		results: []Resolved{
			{
				ID:            "same",
				Digest:        testDigest(t, "first"),
				Kind:          protocol.ResourceMCP,
				Configuration: map[string]any{"value": "first"},
			},
			{
				ID:            "same",
				Digest:        testDigest(t, "second"),
				Kind:          protocol.ResourceMCP,
				Configuration: map[string]any{"value": "second"},
			},
		},
	}
	service, err := NewService([]Resolver{resolver}, nil)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	acquireTestResource(t, service, `{}`)
	_, err = service.AcquireResource(
		t.Context(),
		protocol.ResourceAcquireRequest{
			Kind:          protocol.ResourceMCP,
			Configuration: json.RawMessage(`{}`),
		},
		nil,
	)
	if err == nil {
		t.Fatal("identity collision succeeded")
	}
}

func TestAcquireRemembersIdentityCollisionAfterRelease(t *testing.T) {
	resolver := &changingResolver{
		results: []Resolved{
			{
				ID:            "same",
				Digest:        testDigest(t, "first"),
				Kind:          protocol.ResourceMCP,
				Configuration: map[string]any{"value": "first"},
			},
			{
				ID:            "same",
				Digest:        testDigest(t, "second"),
				Kind:          protocol.ResourceMCP,
				Configuration: map[string]any{"value": "second"},
			},
		},
	}
	service, err := NewService([]Resolver{resolver}, nil)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	first := acquireTestResource(t, service, `{}`)
	if err := first.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	_, err = service.AcquireResource(
		t.Context(),
		protocol.ResourceAcquireRequest{
			Kind:          protocol.ResourceMCP,
			Configuration: json.RawMessage(`{}`),
		},
		nil,
	)
	if err == nil {
		t.Fatal("identity collision after release succeeded")
	}
}

func TestReleaseRevokesLeaseEvenWhenContextIsCanceled(t *testing.T) {
	service := newTestService(t)
	lease := acquireTestResource(
		t,
		service,
		testMCPConfiguration("https://example.invalid", `{}`),
	)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := lease.Release(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("release error = %v, want context canceled", err)
	}
	if got := service.Snapshot(); got != (Snapshot{}) {
		t.Fatalf("snapshot after canceled release = %#v", got)
	}
}

func TestShutdownRevokesAllLeasesAndRefusesAcquisition(t *testing.T) {
	service := newTestService(t)
	lease := acquireTestResource(
		t,
		service,
		testMCPConfiguration("https://example.invalid", `{}`),
	)

	if err := service.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}

	select {
	case <-lease.Done():
	default:
		t.Fatal("lease did not close during shutdown")
	}
	if got := service.Snapshot(); got != (Snapshot{}) {
		t.Fatalf("snapshot after shutdown = %#v", got)
	}
	_, err := service.AcquireResource(
		t.Context(),
		protocol.ResourceAcquireRequest{
			Kind:          protocol.ResourceMCP,
			Configuration: json.RawMessage(`{}`),
		},
		nil,
	)
	if err == nil {
		t.Fatal("acquisition after shutdown succeeded")
	}
}

func TestReleaseCancelsResourceStreamOpening(t *testing.T) {
	hashes := resourcehash.NewService()
	resolver, err := NewMCPResolver(hashes)
	if err != nil {
		t.Fatalf("new resolver: %v", err)
	}
	opener := &blockingResourceOpener{
		started: make(chan struct{}),
	}
	service, err := NewService(
		[]Resolver{resolver},
		[]ResourceOpener{opener},
	)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	lease := acquireTestResource(
		t,
		service,
		testMCPConfiguration("https://example.invalid", `{}`),
	)

	result := make(chan error, 1)
	go func() {
		_, err := service.OpenResource(
			t.Context(),
			protocol.ResourceMCP,
			lease.ResourceID(),
			lease.LeaseID(),
		)
		result <- err
	}()
	<-opener.started

	if err := lease.Release(t.Context()); err != nil {
		t.Fatalf("release lease: %v", err)
	}
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("stream-open error = %v, want context canceled", err)
	}
}

func TestSnapshotsIncludeOpenerRuntimeAfterFinalLease(t *testing.T) {
	hashes := resourcehash.NewService()
	resolver, err := NewMCPResolver(hashes)
	if err != nil {
		t.Fatalf("new resolver: %v", err)
	}
	opener := &retainingResourceOpener{}
	service, err := NewService(
		[]Resolver{resolver},
		[]ResourceOpener{opener},
	)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	lease := acquireTestResource(
		t,
		service,
		testMCPConfiguration("https://example.invalid", `{}`),
	)
	id := lease.ResourceID()
	if err := lease.Release(t.Context()); err != nil {
		t.Fatalf("release lease: %v", err)
	}

	if got := service.Snapshot(); got != (Snapshot{
		ActiveResources: 1,
	}) {
		t.Fatalf("snapshot with retained runtime = %#v", got)
	}
	items := service.ResourceItems()
	if len(items) != 1 ||
		items[0].ID != id ||
		items[0].Kind != protocol.ResourceMCP ||
		items[0].ActiveLeases != 0 {
		t.Fatalf("retained runtime items = %#v", items)
	}

	opener.active = false
	if got := service.Snapshot(); got != (Snapshot{}) {
		t.Fatalf("snapshot after runtime expiry = %#v", got)
	}
}

func newTestService(t *testing.T) *Service {
	t.Helper()

	hashes := resourcehash.NewService()
	resolver, err := NewMCPResolver(hashes)
	if err != nil {
		t.Fatalf("new resolver: %v", err)
	}
	service, err := NewService([]Resolver{resolver}, nil)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	return service
}

func testMCPConfiguration(url, headers string) string {
	return `{"type":"configured","server":{"type":"remote",` +
		`"transport":"http","url":` + fmt.Sprintf("%q", url) +
		`,"headers":` + headers + `}}`
}

func acquireTestResource(
	t *testing.T,
	service *Service,
	configuration string,
) *Lease {
	t.Helper()

	lease, err := service.AcquireResource(
		t.Context(),
		protocol.ResourceAcquireRequest{
			Kind:          protocol.ResourceMCP,
			Configuration: json.RawMessage(configuration),
		},
		nil,
	)
	if err != nil {
		t.Fatalf("acquire resource: %v", err)
	}
	typed, ok := lease.(*Lease)
	if !ok {
		t.Fatalf("lease type = %T", lease)
	}

	return typed
}

func testDigest(t *testing.T, value string) resourcehash.Digest {
	t.Helper()

	digest, err := resourcehash.NewService().Sum(value)
	if err != nil {
		t.Fatalf("hash test resource: %v", err)
	}

	return digest
}

type changingResolver struct {
	results []Resolved
}

func (r *changingResolver) Kind() protocol.ResourceKind {
	return protocol.ResourceMCP
}

func (r *changingResolver) Resolve(
	context.Context,
	json.RawMessage,
) (Resolved, error) {
	result := r.results[0]
	r.results = r.results[1:]
	return result, nil
}

type retainingResourceOpener struct {
	id     protocol.ResourceID
	active bool
}

var _ RuntimeLifecycle = (*retainingResourceOpener)(nil)
var _ RuntimeLister = (*retainingResourceOpener)(nil)

func (*retainingResourceOpener) Kind() protocol.ResourceKind {
	return protocol.ResourceMCP
}

func (*retainingResourceOpener) Open(
	context.Context,
	StreamRequest,
) (agentserver.ResourceStream, error) {
	return nil, errors.New("not implemented")
}

func (h *retainingResourceOpener) LeaseAcquired(resource Resolved) {
	h.id = resource.ID
	h.active = true
}

func (*retainingResourceOpener) LeaseReleased(Resolved) {}

func (h *retainingResourceOpener) Shutdown(context.Context) error {
	h.active = false
	return nil
}

func (h *retainingResourceOpener) RuntimeResourceIDs() []protocol.ResourceID {
	if !h.active {
		return nil
	}

	return []protocol.ResourceID{h.id}
}

type blockingResourceOpener struct {
	started chan struct{}
}

func (*blockingResourceOpener) Kind() protocol.ResourceKind {
	return protocol.ResourceMCP
}

func (h *blockingResourceOpener) Open(
	ctx context.Context,
	_ StreamRequest,
) (agentserver.ResourceStream, error) {
	close(h.started)
	<-ctx.Done()
	return nil, ctx.Err()
}
