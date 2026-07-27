//go:build linux

package safefs

// Tests best-effort ownership and atomicity for regular-file replacement.

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"golang.org/x/sys/unix"
)

func TestReplaceFileOwnedAppliesModeAndBestEffortOwnership(t *testing.T) {
	directory, path := testDirectory(t)
	target := filepath.Join(path, "config")
	gid := os.Getegid()
	groups, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range groups {
		if candidate != os.Getegid() {
			gid = candidate
			break
		}
	}

	if err := directory.ReplaceFileOwned(
		"config",
		[]byte("complete configuration"),
		0o640,
		os.Geteuid(),
		gid,
	); err != nil {
		t.Fatal(err)
	}

	var stat unix.Stat_t
	if err := unix.Lstat(target, &stat); err != nil {
		t.Fatal(err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		t.Fatalf("target type = %o, want regular file", stat.Mode&unix.S_IFMT)
	}
	if stat.Mode&0o777 != 0o640 {
		t.Fatalf("target mode = %04o, want 0640", stat.Mode&0o777)
	}
	if int(stat.Uid) != os.Geteuid() {
		t.Fatalf("target UID = %d, want %d", stat.Uid, os.Geteuid())
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "complete configuration" {
		t.Fatalf("target data = %q", data)
	}
	assertNoTemporaryNames(t, path)
}

func TestReplaceFileOwnedRejectsInvalidOwnership(t *testing.T) {
	directory, path := testDirectory(t)

	type ownershipCase struct {
		name string
		uid  int
		gid  int
	}
	tests := []ownershipCase{
		{name: "negative UID", uid: -1, gid: os.Getegid()},
		{name: "negative GID", uid: os.Geteuid(), gid: -1},
	}
	if strconv.IntSize > 32 {
		omittedID := int(uint64(^uint32(0)))
		tests = append(
			tests,
			ownershipCase{name: "omitted UID sentinel", uid: omittedID, gid: os.Getegid()},
			ownershipCase{name: "omitted GID sentinel", uid: os.Geteuid(), gid: omittedID},
		)
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			name := fmt.Sprintf("invalid-%d", index)
			err := directory.ReplaceFileOwned(name, []byte("bad"), 0o600, test.uid, test.gid)
			if !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("ReplaceFileOwned error = %v, want ErrUnsafePath", err)
			}
			if _, err := os.Lstat(filepath.Join(path, name)); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("invalid replacement target exists: %v", err)
			}
		})
	}
	assertNoTemporaryNames(t, path)
}

func TestReplaceFileOwnedReplacesTargetWithDifferentUID(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("changing a target to a different UID requires root")
	}

	directory, path := testDirectory(t)
	target := filepath.Join(path, "config")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(target, 12345, os.Getegid()); err != nil {
		t.Fatal(err)
	}

	if err := directory.ReplaceFileOwned(
		"config",
		[]byte("replacement"),
		0o600,
		os.Geteuid(),
		os.Getegid(),
	); err != nil {
		t.Fatal(err)
	}

	data, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "replacement" {
		t.Fatalf("target data = %q, want replacement", data)
	}
	assertNoTemporaryNames(t, path)
}

func TestReplaceFileOwnedNormalizesExistingGID(t *testing.T) {
	groups, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	alternateGID := -1
	for _, gid := range groups {
		if gid != os.Getegid() {
			alternateGID = gid
			break
		}
	}
	if alternateGID < 0 {
		t.Skip("normalizing an existing GID requires a supplementary group")
	}

	directory, path := testDirectory(t)
	target := filepath.Join(path, "config")
	if err := os.WriteFile(target, []byte("original"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(target, os.Geteuid(), alternateGID); err != nil {
		t.Skipf("changing the test target group is unavailable: %v", err)
	}

	if err := directory.ReplaceFileOwned(
		"config",
		[]byte("replacement"),
		0o600,
		os.Geteuid(),
		os.Getegid(),
	); err != nil {
		t.Fatal(err)
	}

	var stat unix.Stat_t
	if err := unix.Lstat(target, &stat); err != nil {
		t.Fatal(err)
	}
	if int(stat.Uid) != os.Geteuid() || int(stat.Gid) != os.Getegid() {
		t.Fatalf(
			"target ownership = %d:%d, want %d:%d",
			stat.Uid,
			stat.Gid,
			os.Geteuid(),
			os.Getegid(),
		)
	}
}

func TestReplaceFileOwnedRejectsSymlinkAndNonRegularTargets(t *testing.T) {
	directory, path := testDirectory(t)
	outside := filepath.Join(filepath.Dir(path), filepath.Base(path)+"-owned-outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(outside); err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("remove outside target: %v", err)
		}
	})

	if err := os.Symlink(outside, filepath.Join(path, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(path, "directory"), 0o700); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"link", "directory"} {
		err := directory.ReplaceFileOwned(name, []byte("bad"), 0o600, os.Geteuid(), os.Getegid())
		if !errors.Is(err, ErrUnsafePath) {
			t.Errorf("ReplaceFileOwned(%q) error = %v, want ErrUnsafePath", name, err)
		}
	}

	data, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "outside" {
		t.Fatalf("outside target data = %q", data)
	}
	assertNoTemporaryNames(t, path)
}

func TestReplaceFileOwnedConcurrentResultsAreComplete(t *testing.T) {
	directory, path := testDirectory(t)
	target := filepath.Join(path, "config")

	const writers = 12
	payloads := make([][]byte, 0, writers+1)
	payloads = append(payloads, bytes.Repeat([]byte("initial|"), 1<<14))
	for index := range writers {
		prefix := []byte(fmt.Sprintf("writer-%02d|", index))
		payloads = append(payloads, bytes.Repeat(prefix, 1<<14))
	}
	if err := directory.ReplaceFileOwned(
		"config",
		payloads[0],
		0o600,
		os.Geteuid(),
		os.Getegid(),
	); err != nil {
		t.Fatal(err)
	}

	allowed := make(map[string]struct{}, len(payloads))
	for _, payload := range payloads {
		allowed[string(payload)] = struct{}{}
	}

	start := make(chan struct{})
	writerErrors := make(chan error, writers)
	var wait sync.WaitGroup
	for index := range writers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start

			writerErrors <- directory.ReplaceFileOwned(
				"config",
				payloads[index+1],
				0o600,
				os.Geteuid(),
				os.Getegid(),
			)
		}()
	}

	readDone := make(chan struct{})
	readerError := make(chan error, 1)
	go func() {
		<-start
		for {
			data, err := os.ReadFile(target)
			if err != nil {
				readerError <- err
				return
			}
			if _, ok := allowed[string(data)]; !ok {
				readerError <- fmt.Errorf("observed torn replacement of %d bytes", len(data))
				return
			}

			select {
			case <-readDone:
				readerError <- nil
				return
			default:
			}
		}
	}()

	close(start)
	wait.Wait()
	close(writerErrors)
	close(readDone)

	for err := range writerErrors {
		if err != nil {
			t.Errorf("ReplaceFileOwned: %v", err)
		}
	}
	if err := <-readerError; err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := allowed[string(data)]; !ok {
		t.Fatalf("final replacement is torn: %d bytes", len(data))
	}
	assertNoTemporaryNames(t, path)
}
