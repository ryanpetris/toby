//go:build linux

package toolfiles

// Exercises native target mapping, descriptor authority, complete preflight,
// exact metadata, and concurrent untorn last-launch-wins replacement.

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/configpatch"
	"petris.dev/toby/internal/sandbox/bwrap"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/sandbox/mount"
)

func TestWriterMapsHomeAndManagedFilesWithExactMetadata(t *testing.T) {
	fixture := newWriterFixture(t)

	configParent := filepath.Join(fixture.plan.Home.HostPath, ".config")
	if err := os.Mkdir(configParent, 0o755); err != nil {
		t.Fatal(err)
	}

	files := []File{
		{
			Owner:  "opencode",
			Target: fixture.plan.ManagedDirectories[0].Target + "/native.json",
			Data:   []byte("managed"),
			Mode:   0o640,
			UID:    fixture.plan.Identity.HostUID,
			GID:    fixture.plan.Identity.HostGID,
		},
		{
			Owner:  "opencode",
			Target: "/toby/home/.config/opencode/opencode.json",
			Data:   []byte("home"),
			Mode:   0o600,
			UID:    fixture.plan.Identity.HostUID,
			GID:    fixture.plan.Identity.HostGID,
		},
	}

	generated, err := NewWriter(nil).Write(fixture.plan, fixture.sources, files)
	if err != nil {
		t.Fatal(err)
	}
	if targets := []string{generated[0].Target, generated[1].Target}; !reflect.DeepEqual(
		targets,
		[]string{files[1].Target, files[0].Target},
	) {
		t.Fatalf("generated target order = %q", targets)
	}

	assertNativeFile(
		t,
		filepath.Join(fixture.plan.Home.HostPath, ".config/opencode/opencode.json"),
		"home",
		0o600,
		fixture.plan.Identity,
	)
	assertNativeFile(
		t,
		filepath.Join(fixture.plan.ManagedDirectories[0].HostPath, "native.json"),
		"managed",
		0o640,
		fixture.plan.Identity,
	)

	info, err := os.Stat(configParent)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("existing config parent mode = %04o, want 0755 unchanged", got)
	}

	files[0].Data[0] = 'X'
	generated[0].Data[0] = 'X'
	content, err := os.ReadFile(filepath.Join(fixture.plan.ManagedDirectories[0].HostPath, "native.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "managed" {
		t.Fatalf("published data aliases input or result: %q", content)
	}
}

func TestWriterValidatesCompleteSetBeforeMutation(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*testing.T, *writerFixture) File
	}{
		{
			name: "symlink parent",
			prepare: func(t *testing.T, fixture *writerFixture) File {
				t.Helper()
				external := t.TempDir()
				if err := os.Symlink(
					external,
					filepath.Join(fixture.plan.Home.HostPath, ".z"),
				); err != nil {
					t.Fatal(err)
				}
				return nativeFile(fixture.plan, "/toby/home/.z/config", "bad")
			},
		},
		{
			name: "foreign identity",
			prepare: func(_ *testing.T, fixture *writerFixture) File {
				file := nativeFile(fixture.plan, "/toby/home/.z-config", "bad")
				file.GID++
				return file
			},
		},
		{
			name: "non-native target",
			prepare: func(_ *testing.T, fixture *writerFixture) File {
				return nativeFile(fixture.plan, "/etc/toby/config", "bad")
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newWriterFixture(t)
			good := nativeFile(fixture.plan, "/toby/home/.a/good", "good")
			bad := test.prepare(t, fixture)

			if _, err := NewWriter(nil).Write(
				fixture.plan,
				fixture.sources,
				[]File{good, bad},
			); err == nil {
				t.Fatal("unsafe complete file set was accepted")
			}

			goodPath := filepath.Join(fixture.plan.Home.HostPath, ".a/good")
			if _, err := os.Lstat(goodPath); !os.IsNotExist(err) {
				t.Fatalf("first file was mutated before later validation failed: %v", err)
			}
		})
	}
}

func TestWriterReplacesFinalSymlinkWithoutFollowingIt(t *testing.T) {
	fixture := newWriterFixture(t)
	external := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(external, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(fixture.plan.Home.HostPath, ".app", "config.toml")
	if err := os.Mkdir(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, target); err != nil {
		t.Fatal(err)
	}

	file := nativeFile(
		fixture.plan,
		"/toby/home/.app/config.toml",
		"generated",
	)
	if _, err := NewWriter(nil).Write(
		fixture.plan,
		fixture.sources,
		[]File{file},
	); err != nil {
		t.Fatal(err)
	}

	info, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("replacement target mode = %v, want regular file", info.Mode())
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "generated" {
		t.Fatalf("replacement target data = %q", content)
	}
	externalContent, err := os.ReadFile(external)
	if err != nil {
		t.Fatal(err)
	}
	if string(externalContent) != "outside" {
		t.Fatalf("symlink target data = %q, want unchanged", externalContent)
	}
}

func TestWriterUsesDescriptorInsteadOfDiagnosticHostPath(t *testing.T) {
	fixture := newWriterFixture(t)
	original := fixture.plan.Home.HostPath
	moved := original + "-moved"
	if err := os.Rename(original, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(original, 0o700); err != nil {
		t.Fatal(err)
	}

	file := nativeFile(fixture.plan, "/toby/home/.codex/config.toml", "native")
	generated, err := NewWriter(nil).Write(
		fixture.plan,
		fixture.sources,
		[]File{file},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := generated[0].HostPath, filepath.Join(original, ".codex/config.toml"); got != want {
		t.Fatalf("diagnostic host path = %q, want %q", got, want)
	}

	content, err := os.ReadFile(filepath.Join(moved, ".codex/config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "native" {
		t.Fatalf("descriptor-selected content = %q", content)
	}
	if _, err := os.Lstat(filepath.Join(original, ".codex/config.toml")); !os.IsNotExist(err) {
		t.Fatalf("diagnostic path was reopened as authority: %v", err)
	}
}

func TestWriterRejectsAliasedBackingDescriptorsBeforeMutation(t *testing.T) {
	fixture := newWriterFixture(t)
	aliased := fixture.sources
	aliased.ManagedDirectories = map[mount.Key]*os.File{
		fixture.plan.ManagedDirectories[0].Key: fixture.sources.Home,
	}

	file := nativeFile(fixture.plan, "/toby/home/.codex/config.toml", "native")
	if _, err := NewWriter(nil).Write(fixture.plan, aliased, []File{file}); err == nil {
		t.Fatal("aliased private-home and managed-directory descriptors were accepted")
	}
	if _, err := os.Lstat(
		filepath.Join(fixture.plan.Home.HostPath, ".codex/config.toml"),
	); !os.IsNotExist(err) {
		t.Fatalf("file was mutated before backing validation failed: %v", err)
	}
}

func TestWriterConcurrentReplacementIsUntornLastLaunchWins(t *testing.T) {
	fixture := newWriterFixture(t)
	target := "/toby/home/.config/opencode/opencode.json"
	parent := filepath.Join(fixture.plan.Home.HostPath, ".config/opencode")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatal(err)
	}

	first := bytes.Repeat([]byte("A"), 2<<20)
	second := bytes.Repeat([]byte("B"), 2<<20)
	start := make(chan struct{})
	errors := make(chan error, 12)
	var writers sync.WaitGroup
	for index := range 12 {
		writers.Add(1)
		go func() {
			defer writers.Done()
			<-start

			data := first
			if index%2 != 0 {
				data = second
			}
			_, err := NewWriter(nil).Write(
				fixture.plan,
				fixture.sources,
				[]File{nativeFile(fixture.plan, target, string(data))},
			)
			errors <- err
		}()
	}

	close(start)
	writers.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Error(err)
		}
	}
	if t.Failed() {
		return
	}

	got, err := os.ReadFile(filepath.Join(parent, "opencode.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, first) && !bytes.Equal(got, second) {
		t.Fatalf("concurrent replacement produced %d torn bytes", len(got))
	}
}

func TestWriterLaterLaunchWinsWhileApplicationDatabaseLockIsHeld(
	t *testing.T,
) {
	fixture := newWriterFixture(t)
	target := "/toby/home/.config/opencode/opencode.json"
	configPath := filepath.Join(
		fixture.plan.Home.HostPath,
		".config/opencode/opencode.json",
	)
	databasePath := filepath.Join(
		fixture.plan.ManagedDirectories[0].HostPath,
		"opencode.db",
	)
	database, err := os.OpenFile(
		databasePath,
		os.O_CREATE|os.O_RDWR,
		0o600,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	before, err := database.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(int(database.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer unix.Flock(int(database.Fd()), unix.LOCK_UN)

	if _, err := NewWriter(nil).Write(
		fixture.plan,
		fixture.sources,
		[]File{nativeFile(fixture.plan, target, "launch-a")},
	); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := NewWriter(nil).Write(
			fixture.plan,
			fixture.sources,
			[]File{nativeFile(fixture.plan, target, "launch-b")},
		)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("later launch waited on the application database lock")
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "launch-b" {
		t.Fatalf("final generated config = %q, want later launch", data)
	}
	after, err := database.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("generated-file replacement changed the database inode")
	}
}

func TestWriterAppliesTOMLEnsurePatchToExistingFile(t *testing.T) {
	fixture := newWriterFixture(t)
	parent := filepath.Join(fixture.plan.Home.HostPath, ".grok")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	existing := []byte("" +
		"[ui]\n" +
		"max_thoughts_width = 120\n" +
		"\n" +
		"[plugins]\n" +
		"enabled = [\"already\"]\n")
	if err := os.WriteFile(filepath.Join(parent, "config.toml"), existing, 0o600); err != nil {
		t.Fatal(err)
	}

	generated, err := NewWriter(nil).Write(
		fixture.plan,
		fixture.sources,
		[]File{pluginEnablementFile(fixture.plan)},
	)
	if err != nil {
		t.Fatal(err)
	}
	if string(generated[0].Data) == string(existing) {
		t.Fatal("generated record was not updated with the patched document")
	}

	got, err := os.ReadFile(filepath.Join(parent, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte("already")) || !bytes.Contains(got, []byte("toby-session")) {
		t.Fatalf("patched config = %q", got)
	}
	if !bytes.Contains(got, []byte("max_thoughts_width = 120")) {
		t.Fatalf("integer was not preserved: %q", got)
	}
}

func TestWriterAppliesPatchToMissingFile(t *testing.T) {
	fixture := newWriterFixture(t)
	generated, err := NewWriter(nil).Write(
		fixture.plan,
		fixture.sources,
		[]File{pluginEnablementFile(fixture.plan)},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(generated[0].Data) == 0 {
		t.Fatal("missing-file patch produced empty generated data")
	}

	got, err := os.ReadFile(filepath.Join(fixture.plan.Home.HostPath, ".grok", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte("toby-session")) {
		t.Fatalf("created config = %q", got)
	}
}

func TestWriterRejectsPatchWhenExistingFileIsSymlink(t *testing.T) {
	fixture := newWriterFixture(t)
	external := filepath.Join(t.TempDir(), "outside.toml")
	if err := os.WriteFile(external, []byte("keep = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(fixture.plan.Home.HostPath, ".grok")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(parent, "config.toml")); err != nil {
		t.Fatal(err)
	}

	if _, err := NewWriter(nil).Write(
		fixture.plan,
		fixture.sources,
		[]File{pluginEnablementFile(fixture.plan)},
	); err == nil {
		t.Fatal("symlink config was patched")
	}

	externalContent, err := os.ReadFile(external)
	if err != nil {
		t.Fatal(err)
	}
	if string(externalContent) != "keep = true\n" {
		t.Fatalf("symlink target data = %q", externalContent)
	}
}

func TestWriterDoesNotWriteWhenPatchFails(t *testing.T) {
	fixture := newWriterFixture(t)
	parent := filepath.Join(fixture.plan.Home.HostPath, ".grok")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "config.toml"), []byte("= invalid"), 0o600); err != nil {
		t.Fatal(err)
	}

	good := nativeFile(fixture.plan, "/toby/home/.a/good", "good")
	if _, err := NewWriter(nil).Write(
		fixture.plan,
		fixture.sources,
		[]File{good, pluginEnablementFile(fixture.plan)},
	); err == nil {
		t.Fatal("invalid patch source was accepted")
	}
	if _, err := os.Lstat(filepath.Join(fixture.plan.Home.HostPath, ".a/good")); !os.IsNotExist(err) {
		t.Fatalf("sibling file was written after patch failed: %v", err)
	}
}

func pluginEnablementFile(plan bwrap.Plan) File {
	file := nativeFile(plan, "/toby/home/.grok/config.toml", "")
	file.Data = nil
	file.Patch = configpatch.Patch{
		Ensure: []configpatch.Value{{
			Path:  "/plugins/enabled",
			Value: "toby-session",
		}},
	}
	return file
}

type writerFixture struct {
	plan    bwrap.Plan
	sources bwrap.Sources
}

func newWriterFixture(t *testing.T) *writerFixture {
	t.Helper()

	root := t.TempDir()
	homePath := filepath.Join(root, "home")
	managedPath := filepath.Join(root, "managed")
	for _, directory := range []string{homePath, managedPath} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	home, err := os.Open(homePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := home.Close(); err != nil {
			t.Error(err)
		}
	})
	managed, err := os.Open(managedPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := managed.Close(); err != nil {
			t.Error(err)
		}
	})

	key := mount.Key{
		Type:    mount.TypeTool,
		Name:    "opencode",
		Purpose: "state",
	}
	plan := bwrap.Plan{
		RunID: "run-toolfiles",
		RootFS: bwrap.RootFS{
			Digest: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			Path:   filepath.Join(root, "rootfs"),
		},
		Overlay: bwrap.Overlay{
			RunStorageDir: filepath.Join(root, "runs"),
			Upper:         filepath.Join(root, "runs/run-toolfiles/upper"),
			Work:          filepath.Join(root, "runs/run-toolfiles/work"),
		},
		Home: bwrap.Home{
			ID:       "home-toolfiles",
			HostPath: homePath,
		},
		ManagedDirectories: []mount.Entry{
			{
				Key:      key,
				Profile:  "default",
				HostPath: managedPath,
				Target:   "/toby/home/.local/share/opencode",
				Access:   mount.AccessRegular,
			},
		},
		SandboxBinary: bwrap.Binary{
			HostPath: "/proc/self/exe",
			Target:   layout.SandboxBinary(),
		},
		Workdir:    layout.Home,
		Identity:   bwrap.Identity{HostUID: os.Geteuid(), HostGID: os.Getegid()},
		Namespaces: bwrap.Namespaces{Network: bwrap.NetworkHost},
		Command: bwrap.Command{
			Argv:         []string{"/bin/true"},
			Mode:         bwrap.ExecutionNonInteractive,
			Capabilities: bwrap.CapabilityDropAll,
		},
	}
	if err := plan.Validate(); err != nil {
		t.Fatalf("fixture plan: %v", err)
	}

	return &writerFixture{
		plan: plan,
		sources: bwrap.Sources{
			Home: home,
			ManagedDirectories: map[mount.Key]*os.File{
				key: managed,
			},
		},
	}
}

func nativeFile(plan bwrap.Plan, target, data string) File {
	return File{
		Owner:  "test-tool",
		Target: target,
		Data:   []byte(data),
		Mode:   0o600,
		UID:    plan.Identity.HostUID,
		GID:    plan.Identity.HostGID,
	}
}

func assertNativeFile(
	t *testing.T,
	name string,
	content string,
	mode fs.FileMode,
	identity bwrap.Identity,
) {
	t.Helper()

	got, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Fatalf("%s content = %q, want %q", name, got, content)
	}

	info, err := os.Lstat(name)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("%s mode = %v, want regular file", name, info.Mode())
	}
	if got := info.Mode().Perm(); got != mode {
		t.Fatalf("%s mode = %04o, want %04o", name, got, mode)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("%s has unexpected stat type %T", name, info.Sys())
	}
	if int(stat.Uid) != identity.HostUID || int(stat.Gid) != identity.HostGID {
		t.Fatalf(
			"%s uid:gid = %d:%d, want %d:%d",
			name,
			stat.Uid,
			stat.Gid,
			identity.HostUID,
			identity.HostGID,
		)
	}
}
