//go:build linux

package safefs

// Provides owner-controlled test roots outside shared temporary directories.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testDirectoryOptions(uid int) DirectoryOptions {
	return DirectoryOptions{
		OwnerUID: uid,
		OwnerGID: os.Getgid(),
	}
}

func testDirectory(t *testing.T) (*Directory, string) {
	t.Helper()

	path := secureTestPath(t)
	directory, err := openTestRoot(
		path,
		testDirectoryOptions(os.Getuid()),
	)
	if err != nil {
		t.Fatalf("open test root: %v", err)
	}
	t.Cleanup(func() {
		if err := directory.Close(); err != nil && !strings.Contains(err.Error(), "file already closed") {
			t.Errorf("close test root: %v", err)
		}
	})
	return directory, path
}

func secureTestPath(t *testing.T) string {
	t.Helper()

	candidates := make([]string, 0, 2)
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, home)
	}
	if working, err := os.Getwd(); err == nil {
		candidates = append(candidates, working)
	}

	var failures []string
	for _, parent := range candidates {
		path, err := os.MkdirTemp(parent, ".toby-safefs-test-")
		if err != nil {
			failures = append(failures, err.Error())
			continue
		}
		if err := os.Chmod(path, 0o700); err != nil {
			os.RemoveAll(path)
			failures = append(failures, err.Error())
			continue
		}

		probe, err := openTestRoot(
			path,
			testDirectoryOptions(os.Getuid()),
		)
		if err == nil {
			probe.Close()
			t.Cleanup(func() {
				if err := os.RemoveAll(path); err != nil {
					t.Errorf("remove test root %q: %v", path, err)
				}
			})
			return path
		}

		os.RemoveAll(path)
		failures = append(failures, err.Error())
	}

	t.Fatalf("no secure test parent: %s", strings.Join(failures, "; "))
	return ""
}

func openTestRoot(
	path string,
	options DirectoryOptions,
) (*Directory, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	return OpenDirectoryFile(file, path, options)
}

func assertNoTemporaryNames(t *testing.T, path string) {
	t.Helper()

	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".toby-tmp-") {
			t.Errorf("temporary artifact remains: %s", filepath.Join(path, entry.Name()))
		}
	}
}
