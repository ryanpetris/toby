//go:build linux

package resourcehandler

// Exercises lazy preparation, single-flight attachment, disk checkpoints,
// listener independence, output bounds, and failure broadcast.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"petris.dev/toby/internal/agent/protocol"
	"petris.dev/toby/internal/agent/resourcelease"
	"petris.dev/toby/internal/agent/resourcelog"
	agentserver "petris.dev/toby/internal/agent/server"
	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/config/ociresource"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/oci"
	"petris.dev/toby/internal/oci/image"
)

func TestServiceIsDormantUntilPrepareRequest(t *testing.T) {
	fixture := newServiceFixture(t, &fakeImageService{})

	stream, err := fixture.service.Open(t.Context(), fixture.request())
	if err != nil {
		t.Fatal(err)
	}
	if stream == nil {
		t.Fatal("Open returned nil stream")
	}
	if fixture.images.calls() != 0 {
		t.Fatal("Open started the image store")
	}
	if _, err := os.Stat(resourceLogsDir(fixture.paths)); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("state root exists before prepare: %v", err)
	}
}

func TestServiceCoalescesAndLateListenerStartsFromSnapshot(
	t *testing.T,
) {
	release := make(chan struct{})
	progressed := make(chan struct{})
	images := &fakeImageService{
		progressed: progressed,
		release:    release,
	}
	fixture := newServiceFixture(t, images)

	first := fixture.start(t)
	firstAccepted := readAccepted(t, first)
	<-progressed
	firstProgress := readThroughProgress(t, first, 35)
	if firstProgress.CompletedBytes != 35 {
		t.Fatalf("first progress = %#v", firstProgress)
	}

	second := fixture.start(t)
	secondAccepted := readAccepted(t, second)
	if secondAccepted.OperationID != firstAccepted.OperationID {
		t.Fatalf(
			"operation IDs differ: %q != %q",
			secondAccepted.OperationID,
			firstAccepted.OperationID,
		)
	}
	snapshot := second.recv(t)
	if snapshot.Kind != protocol.OCIEventSnapshot {
		t.Fatalf("late-listener event = %q, want snapshot", snapshot.Kind)
	}
	if snapshot.Progress == nil ||
		snapshot.Progress.CompletedBytes != 35 ||
		snapshot.Progress.TotalBytes != 100 {
		t.Fatalf("late-listener snapshot = %#v", snapshot.Progress)
	}

	close(release)
	firstMessages := readUntilTerminal(t, first)
	secondMessages := readUntilTerminal(t, second)
	first.close(t)
	second.close(t)

	if images.calls() != 1 {
		t.Fatalf("Prepare calls = %d, want 1", images.calls())
	}
	assertTerminalSuccess(t, firstMessages)
	assertTerminalSuccess(t, secondMessages)

	logs, err := filepath.Glob(filepath.Join(
		resourceLogsDir(fixture.paths),
		"resource.oci",
		"*",
		"*.jsonl",
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 {
		t.Fatalf("operation logs = %v, want one", logs)
	}
	data, err := os.ReadFile(logs[0])
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var record logRecord
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		if record.Kind == recordProgress &&
			record.Progress != nil &&
			record.Progress.CompletedBytes == 35 {
			found = true
		}
	}
	if !found {
		t.Fatal("disk log does not contain the absolute 35% checkpoint")
	}
}

func TestServiceContinuesAfterEveryListenerDisconnects(t *testing.T) {
	release := make(chan struct{})
	progressed := make(chan struct{})
	images := &fakeImageService{
		progressed: progressed,
		release:    release,
	}
	fixture := newServiceFixture(t, images)

	first := fixture.start(t)
	accepted := readAccepted(t, first)
	<-progressed
	first.disconnect()

	second := fixture.start(t)
	secondAccepted := readAccepted(t, second)
	if secondAccepted.OperationID != accepted.OperationID {
		t.Fatalf(
			"replacement listener operation = %q, want %q",
			secondAccepted.OperationID,
			accepted.OperationID,
		)
	}
	if event := second.recv(t); event.Kind != protocol.OCIEventSnapshot {
		t.Fatalf("replacement listener event = %q, want snapshot", event.Kind)
	}

	close(release)
	messages := readUntilTerminal(t, second)
	second.close(t)
	assertTerminalSuccess(t, messages)
	if images.calls() != 1 {
		t.Fatalf("Prepare calls = %d, want 1", images.calls())
	}
}

func TestOperationBoundsFailureOutputAndStillEmitsTerminal(t *testing.T) {
	images := &fakeImageService{
		prepareErr: errors.New(strings.Repeat("x", 4096)),
	}
	fixture := newServiceFixtureWithOptions(
		t,
		images,
		Options{MaximumLogBytes: 512},
	)

	client := fixture.start(t)
	readAccepted(t, client)
	messages := readUntilTerminal(t, client)
	client.close(t)

	if messages[len(messages)-1].Kind != protocol.OCIEventFailed {
		t.Fatalf(
			"terminal event = %q, want failure",
			messages[len(messages)-1].Kind,
		)
	}
	for _, event := range messages {
		if event.Kind == protocol.OCIEventOutput &&
			len(event.Data) > 512 {
			t.Fatalf("oversized failure output = %d bytes", len(event.Data))
		}
	}
}

func TestOperationBoundsProgressJournalAndRetainsLatestSnapshot(
	t *testing.T,
) {
	file, err := os.CreateTemp(t.TempDir(), "operation-*.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	current := newOperation(
		protocol.OperationID("operation"),
		file,
		512,
		nil,
	)
	t.Cleanup(func() {
		if err := current.close(); err != nil {
			t.Error(err)
		}
	})

	for completed := int64(0); completed <= 100; completed++ {
		if err := current.report(oci.Progress{
			Phase:          oci.ProgressDownloading,
			CompletedBytes: completed,
			TotalBytes:     100,
			TotalItems:     1,
		}); err != nil {
			t.Fatal(err)
		}
	}
	current.complete()

	progress, sequence, offset := current.attachment()
	if progress == nil ||
		progress.CompletedBytes != 100 ||
		progress.TotalBytes != 100 {
		t.Fatalf("latest progress = %#v", progress)
	}
	if sequence == 0 || offset <= 0 || offset > 512 {
		t.Fatalf(
			"progress checkpoint = sequence %d, offset %d",
			sequence,
			offset,
		)
	}
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > 768 {
		t.Fatalf("bounded journal size = %d", info.Size())
	}

	data, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
	var terminal logRecord
	if err := json.Unmarshal(lines[len(lines)-1], &terminal); err != nil {
		t.Fatal(err)
	}
	if terminal.Kind != recordComplete {
		t.Fatalf("terminal record = %#v", terminal)
	}
}

func TestServiceBroadcastsPreparationFailure(t *testing.T) {
	release := make(chan struct{})
	progressed := make(chan struct{})
	images := &fakeImageService{
		progressed: progressed,
		release:    release,
		prepareErr: errors.New("injected failure"),
	}
	fixture := newServiceFixture(t, images)

	first := fixture.start(t)
	readAccepted(t, first)
	<-progressed
	second := fixture.start(t)
	readAccepted(t, second)

	close(release)
	for _, client := range []*testOCIClient{first, second} {
		messages := readUntilTerminal(t, client)
		var diagnostic bool
		for _, event := range messages {
			if event.Kind == protocol.OCIEventOutput &&
				bytes.Contains(event.Data, []byte("injected failure")) {
				diagnostic = true
			}
		}
		if !diagnostic {
			t.Fatal("attached listener did not receive failure diagnostic")
		}
		if messages[len(messages)-1].Kind != protocol.OCIEventFailed {
			t.Fatalf(
				"terminal event = %q, want failure",
				messages[len(messages)-1].Kind,
			)
		}
		client.close(t)
	}
	if images.calls() != 1 {
		t.Fatalf("Prepare calls = %d, want 1", images.calls())
	}
}

type serviceFixture struct {
	service *Service
	images  *fakeImageService
	paths   config.Paths
}

func newServiceFixture(
	t *testing.T,
	images *fakeImageService,
) serviceFixture {
	t.Helper()
	return newServiceFixtureWithOptions(t, images, Options{})
}

func newServiceFixtureWithOptions(
	t *testing.T,
	images *fakeImageService,
	options Options,
) serviceFixture {
	t.Helper()

	root := t.TempDir()
	paths := config.Paths{
		Home:         root,
		XDGCacheHome: filepath.Join(root, "cache"),
		XDGDataHome:  filepath.Join(root, "data"),
	}
	options.newBackend = func(
		config.Paths,
		*diagnostic.Service,
	) (imageBackend, error) {
		return images, nil
	}
	service, err := New(
		paths,
		resourcelog.NewService(paths, nil),
		nil,
		options,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := service.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})

	return serviceFixture{
		service: service,
		images:  images,
		paths:   paths,
	}
}

func (f serviceFixture) request() resourcelease.StreamRequest {
	return resourcelease.StreamRequest{
		Resource: resourcelease.Resolved{
			ID:   "resource-test",
			Kind: protocol.ResourceOCI,
			Configuration: ociresource.Config{
				Reference: "registry.example/test:latest",
				Platform: ocispec.Platform{
					OS:           "linux",
					Architecture: "amd64",
				},
				PullPolicy: image.PullIfMissing,
			},
		},
	}
}

func (f serviceFixture) start(t *testing.T) *testOCIClient {
	t.Helper()

	resourceStream, err := f.service.Open(t.Context(), f.request())
	if err != nil {
		t.Fatal(err)
	}
	stream, ok := resourceStream.(agentserver.OCIResourceStream)
	if !ok {
		t.Fatal("OCI service returned a non-OCI resource stream")
	}
	ctx, cancel := context.WithCancel(t.Context())
	events := make(chan protocol.OCIEvent)
	done := make(chan error, 1)
	go func() {
		defer close(events)
		done <- stream.Follow(ctx, func(event protocol.OCIEvent) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case events <- event:
				return nil
			}
		})
	}()

	return &testOCIClient{
		stream: stream,
		events: events,
		cancel: cancel,
		done:   done,
	}
}

type testOCIClient struct {
	stream agentserver.OCIResourceStream
	events <-chan protocol.OCIEvent
	cancel context.CancelFunc
	done   <-chan error
}

func (c *testOCIClient) close(t *testing.T) {
	t.Helper()
	c.cancel()
	if err := c.stream.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-c.done; err != nil &&
		!errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func (c *testOCIClient) disconnect() {
	c.cancel()
	_ = c.stream.Close()
	<-c.done
}

func readAccepted(
	t *testing.T,
	client *testOCIClient,
) protocol.OCIEvent {
	t.Helper()

	event := client.recv(t)
	if event.Kind != protocol.OCIEventAccepted {
		t.Fatalf("event = %q, want accepted", event.Kind)
	}

	return event
}

func readThroughProgress(
	t *testing.T,
	client *testOCIClient,
	completed int64,
) protocol.OCIProgressState {
	t.Helper()

	for {
		event := client.recv(t)
		if (event.Kind == protocol.OCIEventSnapshot ||
			event.Kind == protocol.OCIEventProgress) &&
			event.Progress != nil &&
			event.Progress.CompletedBytes == completed {
			return *event.Progress
		}
	}
}

func readUntilTerminal(
	t *testing.T,
	client *testOCIClient,
) []protocol.OCIEvent {
	t.Helper()

	var result []protocol.OCIEvent
	for {
		event := client.recv(t)
		result = append(result, event)
		switch event.Kind {
		case protocol.OCIEventComplete, protocol.OCIEventFailed:
			return result
		}
	}
}

func assertTerminalSuccess(
	t *testing.T,
	events []protocol.OCIEvent,
) {
	t.Helper()
	if events[len(events)-1].Kind != protocol.OCIEventComplete {
		t.Fatalf(
			"terminal event = %q, want completion",
			events[len(events)-1].Kind,
		)
	}
}

func (c *testOCIClient) recv(t *testing.T) protocol.OCIEvent {
	t.Helper()

	select {
	case event, ok := <-c.events:
		if !ok {
			t.Fatal("OCI event stream ended without a terminal event")
		}
		return event
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
		return protocol.OCIEvent{}
	}
}

type fakeImageService struct {
	mu         sync.Mutex
	prepare    int
	progressed chan struct{}
	release    <-chan struct{}
	prepareErr error
}

func resourceLogsDir(paths config.Paths) string {
	return filepath.Join(paths.TobyCacheDir(), "logs")
}

func (s *fakeImageService) Prepare(
	ctx context.Context,
	request oci.Request,
) (io.Closer, error) {
	s.mu.Lock()
	s.prepare++
	progressed := s.progressed
	s.progressed = nil
	s.mu.Unlock()

	if err := request.Progress(oci.Progress{
		Phase: oci.ProgressResolving,
	}); err != nil {
		return nil, err
	}
	if err := request.Progress(oci.Progress{
		Phase:          oci.ProgressDownloading,
		CompletedBytes: 35,
		TotalBytes:     100,
		TotalItems:     2,
	}); err != nil {
		return nil, err
	}
	if progressed != nil {
		close(progressed)
	}
	if s.release != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-s.release:
		}
	}
	if s.prepareErr != nil {
		return nil, s.prepareErr
	}
	if err := request.Progress(oci.Progress{
		Phase:          oci.ProgressDownloading,
		CompletedBytes: 100,
		TotalBytes:     100,
		CompletedItems: 2,
		TotalItems:     2,
	}); err != nil {
		return nil, err
	}
	if err := request.Progress(oci.Progress{
		Phase:          oci.ProgressExtracting,
		CompletedBytes: 100,
		TotalBytes:     100,
		CompletedItems: 2,
		TotalItems:     2,
	}); err != nil {
		return nil, err
	}

	return io.NopCloser(bytes.NewReader(nil)), nil
}

func (s *fakeImageService) Close() error {
	return nil
}

func (s *fakeImageService) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.prepare
}
