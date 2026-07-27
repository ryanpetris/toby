//go:build linux

package safefs

// Tests concurrent no-replace publication and private-stage cleanup.

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"golang.org/x/sys/unix"
)

func TestPublishFileHasExactlyOneAtomicWinner(t *testing.T) {
	directory, path := testDirectory(t)

	const publishers = 24
	payloads := make([][]byte, publishers)
	for index := range payloads {
		payloads[index] = bytes.Repeat([]byte(fmt.Sprintf("publisher-%02d|", index)), 4096)
	}

	type result struct {
		index     int
		published bool
		err       error
	}
	start := make(chan struct{})
	results := make(chan result, publishers)
	var wait sync.WaitGroup
	for index := range publishers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			published, err := directory.PublishFile("blob", payloads[index], 0o600)
			results <- result{index: index, published: published, err: err}
		}()
	}
	close(start)
	wait.Wait()
	close(results)

	winner := -1
	for result := range results {
		if result.err != nil {
			t.Errorf("publisher %d: %v", result.index, result.err)
		}
		if result.published {
			if winner != -1 {
				t.Errorf("publishers %d and %d both won", winner, result.index)
			}
			winner = result.index
		}
	}
	if winner == -1 {
		t.Fatal("no publisher won")
	}

	data, err := os.ReadFile(filepath.Join(path, "blob"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, payloads[winner]) {
		t.Fatalf("published data is torn or differs from winner %d", winner)
	}
	assertNoTemporaryNames(t, path)
}

func TestPublishFileExistingTargetLosesWithoutReplacement(t *testing.T) {
	directory, path := testDirectory(t)
	if err := os.WriteFile(filepath.Join(path, "blob"), []byte("winner"), 0o600); err != nil {
		t.Fatal(err)
	}

	published, err := directory.PublishFile("blob", []byte("loser"), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if published {
		t.Fatal("publisher replaced an existing file")
	}
	data, err := os.ReadFile(filepath.Join(path, "blob"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "winner" {
		t.Fatalf("existing data = %q", data)
	}
	assertNoTemporaryNames(t, path)
}

func TestPublishFileRejectsRecoveryInaccessibleMode(t *testing.T) {
	directory, path := testDirectory(t)

	published, err := directory.PublishFile("blob", []byte("data"), 0)
	if err == nil {
		t.Fatal("publication with an owner-inaccessible mode succeeded")
	}
	if published {
		t.Fatal("invalid publication reported success")
	}
	if _, err := os.Lstat(filepath.Join(path, "blob")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid publication target exists: %v", err)
	}
	assertNoTemporaryNames(t, path)
}

func TestPublishDirectoryRejectsReplacedTemporaryInode(t *testing.T) {
	directory, path := testDirectory(t)

	published, err := directory.PublishDirectory("snapshot", 100, func(stage *Directory) error {
		if err := stage.WriteFile("intended", []byte("yes"), 0o600); err != nil {
			return err
		}

		temporaryPath := stage.Path()
		if err := os.Rename(temporaryPath, filepath.Join(path, "attacker-kept-directory")); err != nil {
			return err
		}
		if err := os.Mkdir(temporaryPath, 0o700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(temporaryPath, "replacement"), []byte("bad"), 0o600)
	})
	if published {
		t.Fatal("replaced temporary directory was published")
	}
	if !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("PublishDirectory error = %v, want ErrUnsafePath", err)
	}
	if _, err := os.Lstat(filepath.Join(path, "snapshot")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target exists after temporary swap: %v", err)
	}
	if _, err := os.Stat(filepath.Join(path, "attacker-kept-directory", "intended")); err != nil {
		t.Fatalf("retained original stage is missing: %v", err)
	}
}

func TestPublishDirectoryHasExactlyOneWinnerAndCleansLosers(t *testing.T) {
	directory, path := testDirectory(t)

	const publishers = 12
	type result struct {
		index     int
		published bool
		err       error
	}
	start := make(chan struct{})
	results := make(chan result, publishers)
	var wait sync.WaitGroup
	for index := range publishers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			published, err := directory.PublishDirectory("snapshot", 100, func(stage *Directory) error {
				nested, err := stage.MkdirAll("nested")
				if err != nil {
					return err
				}
				defer nested.Close()
				return nested.WriteFile("publisher", []byte(fmt.Sprintf("%d", index)), 0o600)
			})
			results <- result{index: index, published: published, err: err}
		}()
	}
	close(start)
	wait.Wait()
	close(results)

	winner := -1
	for result := range results {
		if result.err != nil {
			t.Errorf("publisher %d: %v", result.index, result.err)
		}
		if result.published {
			if winner != -1 {
				t.Errorf("publishers %d and %d both won", winner, result.index)
			}
			winner = result.index
		}
	}
	if winner == -1 {
		t.Fatal("no directory publisher won")
	}

	data, err := os.ReadFile(filepath.Join(path, "snapshot", "nested", "publisher"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != fmt.Sprintf("%d", winner) {
		t.Fatalf("published marker = %q, winner = %d", data, winner)
	}
	assertNoTemporaryNames(t, path)
}

func TestPublishDirectoryAllowsNestedPublication(t *testing.T) {
	directory, path := testDirectory(t)

	published, err := directory.PublishDirectory("snapshot", 100, func(stage *Directory) error {
		created, err := stage.PublishFile("metadata.json", []byte("{}\n"), 0o600)
		if err != nil {
			return err
		}
		if !created {
			return errors.New("nested publication unexpectedly lost")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !published {
		t.Fatal("directory publication unexpectedly lost")
	}

	data, err := os.ReadFile(filepath.Join(path, "snapshot", "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{}\n" {
		t.Fatalf("nested publication data = %q", data)
	}
}

func TestPublishDirectorySurvivesRestrictiveUmask(t *testing.T) {
	directory, path := testDirectory(t)

	var published bool
	err := func() error {
		previous := unix.Umask(0o777)
		defer unix.Umask(previous)

		var err error
		published, err = directory.PublishDirectory("snapshot", 100, func(stage *Directory) error {
			return stage.WriteFile("value", []byte("private"), 0o600)
		})
		return err
	}()
	if err != nil {
		t.Fatalf("PublishDirectory under umask 0777: %v", err)
	}
	if !published {
		t.Fatal("directory publication unexpectedly lost")
	}

	data, err := os.ReadFile(filepath.Join(path, "snapshot", "value"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "private" {
		t.Fatalf("published value = %q", data)
	}
}

func TestPublishDirectoryPopulationFailureCleansPrivateStage(t *testing.T) {
	directory, path := testDirectory(t)
	sentinel := errors.New("populate failed")

	published, err := directory.PublishDirectory("snapshot", 100, func(stage *Directory) error {
		nested, err := stage.MkdirAll("one/two")
		if err != nil {
			return err
		}
		defer nested.Close()
		if err := nested.WriteFile("partial", []byte("partial"), 0o600); err != nil {
			return err
		}
		return sentinel
	})
	if published {
		t.Fatal("failed population was published")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want population sentinel", err)
	}
	if _, err := os.Lstat(filepath.Join(path, "snapshot")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("snapshot exists after failed population: %v", err)
	}
	assertNoTemporaryNames(t, path)
}
