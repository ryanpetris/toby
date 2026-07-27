//go:build linux

package safefs

// Tests accessible root following, private child creation, and retained
// directory-descriptor stability.

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/diagnostic"
)

func TestOpenOrCreateRootUsesUmaskForParentsAndSecuresRoot(t *testing.T) {
	base := secureTestPath(t)
	target := filepath.Join(base, "external", "xdg", "toby")

	var directory *Directory
	err := func() error {
		previous := unix.Umask(0o027)
		defer unix.Umask(previous)

		var err error
		directory, err = OpenOrCreateRoot(target, testDirectoryOptions(os.Getuid()))
		return err
	}()
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()

	for _, test := range []struct {
		path string
		mode os.FileMode
	}{
		{path: filepath.Join(base, "external"), mode: 0o750},
		{path: filepath.Join(base, "external", "xdg"), mode: 0o750},
		{path: target, mode: 0o700},
	} {
		info, err := os.Stat(test.path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != test.mode {
			t.Errorf(
				"%s mode = %04o, want %04o",
				test.path,
				info.Mode().Perm(),
				test.mode,
			)
		}
	}
}

func TestOpenOrCreateRootTraversesWriteExecuteOnlyParent(t *testing.T) {
	base := secureTestPath(t)
	parent := filepath.Join(base, "write-execute-only")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o300); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(parent, 0o700); err != nil &&
			!errors.Is(err, os.ErrNotExist) {
			t.Errorf("restore parent mode: %v", err)
		}
	})

	target := filepath.Join(parent, "toby")
	root, err := OpenOrCreateRoot(
		target,
		testDirectoryOptions(os.Getuid()),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("Toby root mode = %04o, want 0700", info.Mode().Perm())
	}
}

func TestOpenOrCreateRootRepairsExistingRootAndFollowsSymlink(t *testing.T) {
	base := secureTestPath(t)

	wrongMode := filepath.Join(base, "wrong-mode")
	if err := os.Mkdir(wrongMode, 0o755); err != nil {
		t.Fatal(err)
	}
	modeRoot, err := OpenOrCreateRoot(wrongMode, testDirectoryOptions(os.Getuid()))
	if err != nil {
		t.Fatal(err)
	}
	if err := modeRoot.Close(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(wrongMode); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o700 {
		t.Fatalf(
			"repaired root mode = %04o, want 0700",
			info.Mode().Perm(),
		)
	}

	external := filepath.Join(base, "external")
	if err := os.Mkdir(external, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(external, 0o751); err != nil {
		t.Fatal(err)
	}
	targetParent := filepath.Join(base, "target-parent")
	if err := os.Mkdir(targetParent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(targetParent, 0o750); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(targetParent, "cache")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(external, "toby")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	linkRoot, err := OpenOrCreateRoot(link, testDirectoryOptions(os.Getuid()))
	if err != nil {
		t.Fatal(err)
	}
	if err := linkRoot.Close(); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		path string
		mode os.FileMode
	}{
		{path: external, mode: 0o751},
		{path: targetParent, mode: 0o750},
		{path: target, mode: 0o700},
	} {
		info, err := os.Stat(test.path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != test.mode {
			t.Errorf(
				"%s mode = %04o, want %04o",
				test.path,
				info.Mode().Perm(),
				test.mode,
			)
		}
	}
	if info, err := os.Lstat(link); err != nil {
		t.Fatal(err)
	} else if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("configured Toby root symlink was replaced")
	}
	if got, err := os.Readlink(link); err != nil {
		t.Fatal(err)
	} else if got != target {
		t.Fatalf("configured Toby root symlink target = %q, want %q", got, target)
	}
}

func TestOpenOrCreateRootLogsWhenOwnershipRepairFailsAndRepairsMode(
	t *testing.T,
) {
	base := secureTestPath(t)
	target := filepath.Join(base, "foreign")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o755); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	options := testDirectoryOptions(os.Getuid() + 1)
	diagnostics, err := diagnostic.NewService(diagnostic.Options{
		Level:  slog.LevelDebug,
		Format: diagnostic.FormatText,
		Stderr: &stderr,
	})
	if err != nil {
		t.Fatal(err)
	}
	options.Logger = diagnostics.Logger("test")
	root, err := OpenOrCreateRoot(target, options)
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(target); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o700 {
		t.Fatalf(
			"root mode = %04o, want repaired 0700",
			info.Mode().Perm(),
		)
	}
	if got := stderr.String(); !strings.Contains(
		got,
		"correct Toby-owned directory ownership",
	) {
		t.Fatalf("ownership repair diagnostic = %q", got)
	}
}

func TestOpenPrivateRootDoesNotCreateMissingRoot(t *testing.T) {
	target := filepath.Join(secureTestPath(t), "missing")

	if _, err := OpenPrivateRoot(target, testDirectoryOptions(os.Getuid())); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing private root error = %v, want not-exist", err)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("OpenPrivateRoot created missing root: %v", err)
	}
}

func TestOpenOrCreateRootConcurrent(t *testing.T) {
	base := secureTestPath(t)
	target := filepath.Join(base, "concurrent", "root")

	const publishers = 16
	var wait sync.WaitGroup
	errorsByPublisher := make(chan error, publishers)
	for range publishers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			directory, err := OpenOrCreateRoot(target, testDirectoryOptions(os.Getuid()))
			if err == nil {
				err = directory.Close()
			}
			errorsByPublisher <- err
		}()
	}
	wait.Wait()
	close(errorsByPublisher)

	for err := range errorsByPublisher {
		if err != nil {
			t.Errorf("concurrent open/create: %v", err)
		}
	}
}

func TestPrivateDirectoryCreationSurvivesRestrictiveUmask(t *testing.T) {
	base := secureTestPath(t)
	target := filepath.Join(base, "root", "nested")
	if err := os.Mkdir(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}

	var root *Directory
	err := func() error {
		previous := unix.Umask(0o777)
		defer unix.Umask(previous)

		var err error
		root, err = OpenOrCreateRoot(target, testDirectoryOptions(os.Getuid()))
		return err
	}()
	if err != nil {
		t.Fatalf("OpenOrCreateRoot under umask 0777: %v", err)
	}
	defer root.Close()

	directory, _ := testDirectory(t)
	var child *Directory
	err = func() error {
		previous := unix.Umask(0o777)
		defer unix.Umask(previous)

		var err error
		child, err = directory.MkdirAll("one/two")
		return err
	}()
	if err != nil {
		t.Fatalf("MkdirAll under umask 0777: %v", err)
	}
	defer child.Close()

	for _, name := range []string{
		filepath.Join(base, "root"),
		target,
		filepath.Join(directory.Path(), "one"),
		filepath.Join(directory.Path(), "one", "two"),
	} {
		info, err := os.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Errorf("%s mode = %04o, want 0700", name, info.Mode().Perm())
		}
	}
}

func TestMkdirAllDoesNotRepairExistingRestrictiveDirectory(
	t *testing.T,
) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permission checks")
	}

	directory, path := testDirectory(t)
	interruptedChild := filepath.Join(path, "interrupted-child")
	if err := os.Mkdir(interruptedChild, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(interruptedChild, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(interruptedChild, 0o700); err != nil &&
			!errors.Is(err, os.ErrNotExist) {
			t.Errorf("restore restrictive directory: %v", err)
		}
	})

	if _, err := directory.MkdirAll(
		"interrupted-child/nested",
	); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("MkdirAll error = %v, want permission error", err)
	}
	info, err := os.Stat(interruptedChild)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0 {
		t.Fatalf(
			"existing directory mode = %04o, want unchanged 0000",
			info.Mode().Perm(),
		)
	}
}

func TestDirectoryCreationFallbackAndRetainedDescriptors(t *testing.T) {
	directory, path := testDirectory(t)

	child, err := directory.MkdirAll("one/two")
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	if child.Path() != filepath.Join(path, "one", "two") {
		t.Fatalf("Path() = %q", child.Path())
	}
	for _, name := range []string{"one", filepath.Join("one", "two")} {
		info, err := os.Stat(filepath.Join(path, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Errorf("%s mode = %04o, want 0700", name, info.Mode().Perm())
		}
	}

	file, err := child.File()
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFD, 0)
	if err != nil {
		t.Fatal(err)
	}
	if flags&unix.FD_CLOEXEC == 0 {
		t.Fatal("duplicated directory descriptor lacks CLOEXEC")
	}

	duplicate, err := child.Duplicate()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Close(); err != nil {
		t.Fatal(err)
	}
	if err := duplicate.WriteFile("after-close", []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := duplicate.Close(); err != nil {
		t.Fatal(err)
	}

	rootFile, err := directory.File()
	if err != nil {
		t.Fatal(err)
	}
	defer rootFile.Close()

	outside := filepath.Join(path, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(path, "one", "link")); err != nil {
		t.Fatal(err)
	}
	if fd, err := openAtWalk(
		int(rootFile.Fd()),
		"one/link",
		unix.O_RDONLY|unix.O_DIRECTORY,
		0,
		nil,
	); err == nil {
		unix.Close(fd)
		t.Fatal("component-walk fallback followed an intermediate symlink")
	}
}

func TestDirectoryCapabilitySurvivesRootRename(t *testing.T) {
	directory, path := testDirectory(t)
	moved := path + "-moved"
	t.Cleanup(func() {
		os.RemoveAll(moved)
	})

	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	if err := directory.WriteFile("stable", []byte("capability"), 0o600); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(moved, "stable"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "capability" {
		t.Fatalf("data = %q", data)
	}
}
