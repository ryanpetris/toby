//go:build linux

package run

// Native launch option copying, storage preparation, and small launch helpers
// for one production Linux run.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"

	"golang.org/x/term"

	appconfig "petris.dev/toby/internal/config/app"
	"petris.dev/toby/internal/diagnostic/exitcode"
	"petris.dev/toby/internal/diagnostic/warning"
	"petris.dev/toby/internal/sandbox/bwrap"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/sandbox/mount"
	"petris.dev/toby/internal/storage"
	"petris.dev/toby/internal/tools"
)

func nativeLaunchOptions(opts tools.Options, primary string) tools.Options {
	options := opts
	options.Projects = append([]tools.ProjectMount(nil), opts.Projects...)
	options.Primary = primary
	return options
}

type nativeLaunchStorage struct {
	volumeStore        *storage.Store
	persistentDataRoot *os.File
	home               *storage.HomeHandle
	managed            []*storage.ManagedHandle
	runStorage         *bwrap.RunStorage
	runStorageRoot     *os.File
	directories        *bwrap.RunDirectories
}

func (s *nativeLaunchStorage) close() error {
	if s == nil {
		return nil
	}

	var closeErr error
	if s.directories != nil {
		closeErr = errors.Join(closeErr, s.directories.Close())
		s.directories = nil
	}
	if s.runStorageRoot != nil {
		closeErr = errors.Join(closeErr, s.runStorageRoot.Close())
		s.runStorageRoot = nil
	}
	if s.runStorage != nil {
		closeErr = errors.Join(closeErr, s.runStorage.Close())
		s.runStorage = nil
	}
	closeErr = errors.Join(closeErr, closeManagedHandles(s.managed))
	s.managed = nil
	if s.home != nil {
		closeErr = errors.Join(closeErr, s.home.Close())
		s.home = nil
	}
	if s.persistentDataRoot != nil {
		closeErr = errors.Join(closeErr, s.persistentDataRoot.Close())
		s.persistentDataRoot = nil
	}
	if s.volumeStore != nil {
		closeErr = errors.Join(closeErr, s.volumeStore.Close())
		s.volumeStore = nil
	}

	return closeErr
}

func (r *NativeRunner) prepareNativeLaunchStorage(
	ctx context.Context,
	env string,
	profile string,
	toolProfiles map[string]string,
	prepared PreparedImage,
	mounts []mount.Request,
	occupied []string,
) (result nativeLaunchStorage, returnErr error) {
	defer func() {
		if returnErr != nil {
			r.logCleanup("close partial launch storage", result.close())
		}
	}()

	homeOperation := r.status.StartOperation("Preparing private home")
	volumeStore, err := storage.NewStore(
		r.paths,
		storage.DefaultLimits(),
		r.diagnostics,
	)
	if err != nil {
		return nativeLaunchStorage{}, err
	}
	result.volumeStore = volumeStore

	persistentDataRoot, err := volumeStore.DataRootFile()
	if err != nil {
		return nativeLaunchStorage{}, err
	}
	result.persistentDataRoot = persistentDataRoot

	rootfsFile, err := prepared.RootfsFile()
	if err != nil {
		return nativeLaunchStorage{}, err
	}
	defer func() {
		r.logCleanup("close OCI rootfs seed", rootfsFile.Close())
	}()
	rootfsSeed := storage.SeedSource{
		Root:            rootfsFile,
		RootDescription: prepared.RootfsPath(),
		ImagePath:       layout.Home,
	}

	home, err := volumeStore.ResolveHome(
		ctx,
		env,
		profile,
		rootfsSeed,
	)
	if err != nil {
		return nativeLaunchStorage{}, err
	}
	result.home = home

	managed, err := volumeStore.ResolveManaged(
		ctx,
		storage.ProfileSelection{
			Default: profile,
			Tools:   toolProfiles,
		},
		mounts,
		occupied,
		rootfsSeed,
	)
	if err != nil {
		return nativeLaunchStorage{}, err
	}
	result.managed = managed

	runStorage, err := bwrap.OpenRunStorage(
		r.paths.RunStorageDir(),
		bwrap.DefaultRunStorageLimits(),
		r.logger,
	)
	if err != nil {
		return nativeLaunchStorage{}, err
	}
	result.runStorage = runStorage

	runStorageRoot, err := runStorage.RootFile()
	if err != nil {
		return nativeLaunchStorage{}, err
	}
	result.runStorageRoot = runStorageRoot

	directories, err := runStorage.Create(ctx)
	if err != nil {
		return nativeLaunchStorage{}, err
	}
	result.directories = directories
	homeOperation.Finish(nil)

	return result, nil
}

func nativeForegroundMode(
	managed bool,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
) bwrap.ExecutionMode {
	input, ok := stdin.(*os.File)
	if !ok || input == nil || !term.IsTerminal(int(input.Fd())) {
		return bwrap.ExecutionNonInteractive
	}

	output, outputTerminal := stdout.(*os.File)
	errorOutput, errorTerminal := stderr.(*os.File)
	if managed &&
		outputTerminal &&
		output != nil &&
		errorTerminal &&
		errorOutput != nil &&
		term.IsTerminal(int(output.Fd())) &&
		term.IsTerminal(int(errorOutput.Fd())) &&
		sameNativeTerminal(input, output, errorOutput) {
		return bwrap.ExecutionManagedPTY
	}

	return bwrap.ExecutionDirectTerminal
}

func nativeTerminalType(mode bwrap.ExecutionMode) string {
	if mode == bwrap.ExecutionNonInteractive {
		return ""
	}

	return os.Getenv("TERM")
}

func sameNativeTerminal(files ...*os.File) bool {
	if len(files) < 2 {
		return true
	}

	first, err := files[0].Stat()
	if err != nil {
		return false
	}
	for _, file := range files[1:] {
		current, err := file.Stat()
		if err != nil || !os.SameFile(first, current) {
			return false
		}
	}
	return true
}

func warnIfNativeAutoDeny(
	warnings *warning.Service,
	config *appconfig.Service,
	mode bwrap.ExecutionMode,
) {
	settings := config.Settings()
	if settings.YoloEnabled() || mode == bwrap.ExecutionManagedPTY {
		return
	}

	reason := "unavailable unless stdin, stdout, and stderr share one terminal"
	if !settings.ManagedTerminalEnabled() {
		reason = "off (settings.managedTerminal is false)"
	}
	warnings.Warn(
		warning.PermissionAutoDeny,
		fmt.Sprintf(
			"approval prompts are %s; actions that are not explicitly allowed will be denied",
			reason,
		),
		"reason", reason,
		"managed_terminal", settings.ManagedTerminalEnabled(),
	)
}

func resolveNativeWorkdir(
	configured string,
	projects []bwrap.Project,
) (string, error) {
	workdir := layout.ExpandHome(configured)
	if workdir == "" {
		if len(projects) == 0 {
			return "", fmt.Errorf("native launch requires a project")
		}
		workdir = projects[0].Target
	}
	if !path.IsAbs(workdir) ||
		path.Clean(workdir) != workdir {
		return "", exitcode.New(
			2,
			"workdir must be a clean absolute sandbox path: %q",
			configured,
		)
	}

	return workdir, nil
}

func nativeOccupiedTargets(
	projects []bwrap.Project,
	binds []NativeBind,
) []string {
	targets := make([]string, 0, len(projects)+len(binds))
	for _, project := range projects {
		targets = append(targets, project.Target)
	}
	for _, bind := range binds {
		targets = append(targets, bind.Bind.Target)
	}

	return targets
}

func nativeManagedEntries(
	handles []*storage.ManagedHandle,
) []mount.Entry {
	entries := make([]mount.Entry, len(handles))
	for index, handle := range handles {
		entries[index] = handle.Entry()
	}

	return entries
}

func nativeManagedInterfaces(
	handles []*storage.ManagedHandle,
) []ManagedDirectory {
	result := make([]ManagedDirectory, len(handles))
	for index, handle := range handles {
		result[index] = handle
	}

	return result
}

func closeManagedHandles(handles []*storage.ManagedHandle) error {
	var closeErr error
	for index := len(handles) - 1; index >= 0; index-- {
		if handles[index] != nil {
			closeErr = errors.Join(closeErr, handles[index].Close())
			handles[index] = nil
		}
	}

	return closeErr
}

func nativeBindPlans(binds []NativeBind) []mount.Bind {
	result := make([]mount.Bind, len(binds))
	for index, bind := range binds {
		result[index] = bind.Bind
	}

	return result
}

func nativeProjectScopeIdentity(project bwrap.Project) string {
	digest := sha256.Sum256(
		[]byte(project.Name + "\x00" + project.HostPath),
	)
	return "project-" + hex.EncodeToString(digest[:])
}
