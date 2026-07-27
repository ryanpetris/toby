package oci

// Verifies absolute aggregation across concurrent artifact readers.

import (
	"bytes"
	"io"
	"os"
	"sync"
	"testing"
)

func TestTransferProgressAggregatesConcurrentArtifacts(t *testing.T) {
	var mu sync.Mutex
	var snapshots []Progress
	progress, err := newTransferProgress(
		ProgressDownloading,
		[]int64{128, 256},
		func(snapshot Progress) error {
			mu.Lock()
			snapshots = append(snapshots, snapshot)
			mu.Unlock()
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	var wait sync.WaitGroup
	for index, size := range []int{128, 256} {
		wait.Add(1)
		go func(index int, size int) {
			defer wait.Done()
			reader := &progressReadCloser{
				ReadCloser: io.NopCloser(bytes.NewReader(
					bytes.Repeat([]byte{byte(index)}, size),
				)),
				progress: progress,
				index:    index,
			}
			if _, err := io.Copy(io.Discard, reader); err != nil {
				t.Error(err)
			}
			if err := reader.Close(); err != nil {
				t.Error(err)
			}
		}(index, size)
	}
	wait.Wait()
	if err := progress.finish(); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(snapshots) < 3 {
		t.Fatalf("progress snapshots = %#v", snapshots)
	}
	var previous int64
	for _, snapshot := range snapshots {
		if snapshot.CompletedBytes < previous {
			t.Fatalf("progress moved backwards: %#v", snapshots)
		}
		if snapshot.TotalBytes != 384 || snapshot.TotalItems != 2 {
			t.Fatalf("progress totals = %#v", snapshot)
		}
		previous = snapshot.CompletedBytes
	}
	final := snapshots[len(snapshots)-1]
	if final.CompletedBytes != 384 || final.CompletedItems != 2 {
		t.Fatalf("final progress = %#v", final)
	}
}

func TestTransferProgressPropagatesReporterFailure(t *testing.T) {
	reportErr := io.ErrClosedPipe
	_, err := newTransferProgress(
		ProgressExtracting,
		[]int64{1},
		func(Progress) error { return reportErr },
	)
	if err != reportErr {
		t.Fatalf("newTransferProgress() error = %v, want %v", err, reportErr)
	}
}

func TestRootlessMapOptionsMapContainerRootToCurrentIdentity(t *testing.T) {
	options := rootlessMapOptions()
	if !options.Rootless {
		t.Fatal("rootless mapping is disabled")
	}
	if len(options.UIDMappings) != 1 ||
		options.UIDMappings[0].ContainerID != 0 ||
		options.UIDMappings[0].HostID != uint32(os.Geteuid()) ||
		options.UIDMappings[0].Size != 1 {
		t.Fatalf("UID mappings = %#v", options.UIDMappings)
	}
	if len(options.GIDMappings) != 1 ||
		options.GIDMappings[0].ContainerID != 0 ||
		options.GIDMappings[0].HostID != uint32(os.Getegid()) ||
		options.GIDMappings[0].Size != 1 {
		t.Fatalf("GID mappings = %#v", options.GIDMappings)
	}
}
