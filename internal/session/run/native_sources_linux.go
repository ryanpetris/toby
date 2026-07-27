//go:build linux

package run

// Opens exact launch-selected project, external-bind, runtime-directory, and
// Toby-binary capabilities before native run assembly.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/executable"
	"petris.dev/toby/internal/sandbox/bwrap"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/sandbox/mount"
	"petris.dev/toby/internal/storage/safefs"
	"petris.dev/toby/internal/tools"
)

func openNativeProjects(
	configured []tools.ProjectMount,
	projectRootPath string,
	logger *diagnostic.Logger,
) ([]NativeProject, []bwrap.Project, error) {
	var projectRoot *os.File
	if projectsRequireRoot(configured) {
		var err error
		projectRoot, err = openNativeDirectory(
			projectRootPath,
			"project root",
			logger,
		)
		if err != nil {
			return nil, nil, fmt.Errorf(
				"open XDG project root: %w",
				err,
			)
		}
	}

	projects, view, err := openNativeProjectsWithRoot(
		configured,
		projectRoot,
		logger,
	)
	if projectRoot == nil {
		return projects, view, err
	}
	logger.DebugError(
		"close XDG project-root descriptor",
		projectRoot.Close(),
	)
	if err != nil {
		return nil, nil, err
	}

	return projects, view, nil
}

func projectsRequireRoot(configured []tools.ProjectMount) bool {
	for _, project := range configured {
		if project.RequireProjectRoot {
			return true
		}
	}

	return false
}

func openNativeProjectsWithRoot(
	configured []tools.ProjectMount,
	projectRoot *os.File,
	logger *diagnostic.Logger,
) ([]NativeProject, []bwrap.Project, error) {
	projects := make([]NativeProject, 0, len(configured))
	view := make([]bwrap.Project, 0, len(configured))
	seen := make(map[string]struct{}, len(configured))

	for _, item := range configured {
		if _, duplicate := seen[item.Name]; duplicate {
			logger.DebugError(
				"close native projects after duplicate name",
				closeNativeProjects(projects),
			)
			return nil, nil, fmt.Errorf(
				"duplicate native project name %q",
				item.Name,
			)
		}
		seen[item.Name] = struct{}{}

		source, err := openNativeDirectory(
			item.Source,
			"project "+item.Name,
			logger,
		)
		if err != nil {
			logger.DebugError(
				"close native projects after project open failed",
				closeNativeProjects(projects),
			)
			return nil, nil, fmt.Errorf(
				"open native project %q: %w",
				item.Name,
				err,
			)
		}
		if item.RequireProjectRoot {
			if projectRoot == nil {
				logger.DebugError(
					"close uncontained native project",
					source.Close(),
				)
				logger.DebugError(
					"close native projects after containment setup failed",
					closeNativeProjects(projects),
				)
				return nil, nil, fmt.Errorf(
					"native project %q requires an XDG project-root descriptor",
					item.Name,
				)
			}

			contained, err := directoryDescriptorContains(
				projectRoot,
				source,
				logger,
			)
			if err != nil {
				logger.DebugError(
					"close invalid native project",
					source.Close(),
				)
				logger.DebugError(
					"close native projects after containment check failed",
					closeNativeProjects(projects),
				)
				return nil, nil, fmt.Errorf(
					"validate native project %q against XDG project root: %w",
					item.Name,
					err,
				)
			}
			if !contained {
				logger.DebugError(
					"close external native project",
					source.Close(),
				)
				logger.DebugError(
					"close native projects after containment rejection",
					closeNativeProjects(projects),
				)
				return nil, nil, fmt.Errorf(
					"native project %q resolves outside XDG_PROJECTS_DIR",
					item.Name,
				)
			}
		}

		target := path.Join(layout.Workspace, item.Name)
		input := bwrap.ProjectInput{
			Name:     item.Name,
			HostPath: item.Source,
		}
		projects = append(projects, NativeProject{
			Input:  input,
			Source: source,
		})
		view = append(view, bwrap.Project{
			Name:     item.Name,
			HostPath: item.Source,
			Target:   target,
		})
	}

	return projects, view, nil
}

func openNativeDirectory(
	hostPath string,
	label string,
	logger *diagnostic.Logger,
) (*os.File, error) {
	descriptor, err := unix.Open(
		hostPath,
		unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return nil, err
	}

	file := os.NewFile(uintptr(descriptor), label)
	if file == nil {
		logger.DebugError(
			"close invalid native directory descriptor",
			unix.Close(descriptor),
			"label", label,
		)
		return nil, fmt.Errorf("invalid %s descriptor", label)
	}

	return file, nil
}

func directoryDescriptorContains(
	root *os.File,
	directory *os.File,
	logger *diagnostic.Logger,
) (bool, error) {
	if root == nil || directory == nil {
		return false, fmt.Errorf("directory descriptor is nil")
	}

	rootInfo, err := root.Stat()
	if err != nil {
		return false, fmt.Errorf("inspect project-root descriptor: %w", err)
	}

	current, err := duplicateFile(directory)
	if err != nil {
		return false, fmt.Errorf("duplicate project descriptor: %w", err)
	}
	defer func() {
		if current != nil {
			logger.DebugError(
				"close project ancestry descriptor",
				current.Close(),
			)
		}
	}()

	const maximumDirectoryDepth = 4096
	for depth := 0; depth < maximumDirectoryDepth; depth++ {
		currentInfo, err := current.Stat()
		if err != nil {
			return false, fmt.Errorf(
				"inspect project ancestry descriptor: %w",
				err,
			)
		}
		if os.SameFile(rootInfo, currentInfo) {
			return true, nil
		}

		parent, err := openNativeDirectoryAt(
			current,
			"..",
			"project ancestry parent",
			logger,
		)
		if err != nil {
			return false, err
		}
		parentInfo, err := parent.Stat()
		if err != nil {
			logger.DebugError(
				"close uninspected project ancestry parent",
				parent.Close(),
			)
			return false, fmt.Errorf(
				"inspect project ancestry parent: %w",
				err,
			)
		}
		if os.SameFile(currentInfo, parentInfo) {
			logger.DebugError(
				"close project filesystem root descriptor",
				parent.Close(),
			)
			return false, nil
		}

		logger.DebugError(
			"close traversed project ancestry descriptor",
			current.Close(),
		)
		current = parent
	}

	return false, fmt.Errorf(
		"project directory ancestry exceeds %d levels",
		maximumDirectoryDepth,
	)
}

func openNativeDirectoryAt(
	parent *os.File,
	relative string,
	label string,
	logger *diagnostic.Logger,
) (*os.File, error) {
	descriptor, err := unix.Openat(
		int(parent.Fd()),
		relative,
		unix.O_PATH|
			unix.O_DIRECTORY|
			unix.O_NOFOLLOW|
			unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", label, err)
	}

	file := os.NewFile(uintptr(descriptor), label)
	if file == nil {
		logger.DebugError(
			"close invalid native child directory descriptor",
			unix.Close(descriptor),
			"label", label,
		)
		return nil, fmt.Errorf("open %s: invalid descriptor", label)
	}

	return file, nil
}

func closeNativeProjects(projects []NativeProject) error {
	var closeErr error
	for index := range projects {
		if projects[index].Source != nil {
			closeErr = errors.Join(
				closeErr,
				projects[index].Source.Close(),
			)
			projects[index].Source = nil
		}
	}

	return closeErr
}

func openNativeBinds(
	configured []mount.Bind,
	logger *diagnostic.Logger,
) (result []NativeBind, returnErr error) {
	if len(configured) == 0 {
		return []NativeBind{}, nil
	}

	rootFD, err := unix.Open(
		"/",
		unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open external-bind resolution root: %w", err)
	}

	binds := make([]NativeBind, 0, len(configured))
	defer func() {
		logger.DebugError(
			"close external-bind resolution root",
			unix.Close(rootFD),
		)
		if returnErr != nil {
			logger.DebugError(
				"close external binds after resolution failure",
				closeNativeBinds(binds),
			)
			result = nil
		}
	}()

	for _, item := range configured {
		if err := item.Validate(); err != nil {
			return nil, fmt.Errorf(
				"validate external bind for %s: %w",
				item.Target,
				err,
			)
		}

		parent, source, resolvedHostPath, err := openNativeBind(
			rootFD,
			item,
			logger,
		)
		if err != nil {
			if item.Optional && errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf(
				"open external bind for %s: %w",
				item.Target,
				err,
			)
		}

		binds = append(binds, NativeBind{
			Bind:         item,
			Source:       source,
			Parent:       parent,
			ResolvedName: filepath.Base(resolvedHostPath),
		})
	}

	return binds, nil
}

func openNativeBind(
	rootFD int,
	item mount.Bind,
	logger *diagnostic.Logger,
) (
	parent *os.File,
	source *os.File,
	resolvedHostPath string,
	returnErr error,
) {
	relativeSource := strings.TrimPrefix(
		item.HostPath,
		string(filepath.Separator),
	)
	if relativeSource == "" {
		return nil, nil, "", fmt.Errorf(
			"external bind host path has no direct-child basename: %q",
			item.HostPath,
		)
	}

	sourceFD, err := unix.Openat2(
		rootFD,
		relativeSource,
		&unix.OpenHow{
			Flags: unix.O_PATH | unix.O_CLOEXEC,
			Resolve: unix.RESOLVE_IN_ROOT |
				unix.RESOLVE_NO_MAGICLINKS,
		},
	)
	if err != nil {
		return nil, nil, "", fmt.Errorf(
			"open external bind source %q: %w",
			item.HostPath,
			err,
		)
	}
	source = os.NewFile(
		uintptr(sourceFD),
		"external bind "+item.Target,
	)
	if source == nil {
		logger.DebugError(
			"close invalid external-bind source descriptor",
			unix.Close(sourceFD),
		)
		return nil, nil, "", fmt.Errorf(
			"create external-bind source descriptor",
		)
	}
	defer func() {
		if returnErr != nil {
			logger.DebugError(
				"close external-bind source after resolution failed",
				source.Close(),
			)
			source = nil
		}
	}()

	resolvedHostPath, err = os.Readlink(
		"/proc/self/fd/" + strconv.FormatUint(
			uint64(source.Fd()),
			10,
		),
	)
	if err != nil {
		return nil, nil, "", fmt.Errorf(
			"resolve external bind source %q: %w",
			item.HostPath,
			err,
		)
	}
	if !filepath.IsAbs(resolvedHostPath) {
		return nil, nil, "", fmt.Errorf(
			"resolved external bind source is not absolute: %q",
			resolvedHostPath,
		)
	}
	resolvedHostPath = filepath.Clean(resolvedHostPath)

	parentPath := filepath.Dir(resolvedHostPath)
	relativeParent := strings.TrimPrefix(
		parentPath,
		string(filepath.Separator),
	)
	if relativeParent == "" {
		relativeParent = "."
	}
	parentFD, err := unix.Openat2(
		rootFD,
		relativeParent,
		&unix.OpenHow{
			Flags: unix.O_PATH |
				unix.O_DIRECTORY |
				unix.O_CLOEXEC,
			Resolve: unix.RESOLVE_BENEATH |
				unix.RESOLVE_NO_MAGICLINKS |
				unix.RESOLVE_NO_SYMLINKS,
		},
	)
	if err != nil {
		return nil, nil, "", fmt.Errorf(
			"open resolved external-bind parent %q: %w",
			parentPath,
			err,
		)
	}
	parent = os.NewFile(
		uintptr(parentFD),
		"external bind "+item.Target+" parent",
	)
	if parent == nil {
		logger.DebugError(
			"close invalid external-bind parent descriptor",
			unix.Close(parentFD),
		)
		return nil, nil, "", fmt.Errorf(
			"create external-bind parent descriptor",
		)
	}
	defer func() {
		if returnErr != nil {
			logger.DebugError(
				"close external-bind parent after resolution failed",
				parent.Close(),
			)
			parent = nil
		}
	}()

	return parent, source, resolvedHostPath, nil
}

func closeNativeBinds(binds []NativeBind) error {
	var closeErr error
	for index := range binds {
		if binds[index].Source != nil {
			closeErr = errors.Join(closeErr, binds[index].Source.Close())
			binds[index].Source = nil
		}
		if binds[index].Parent != nil {
			closeErr = errors.Join(closeErr, binds[index].Parent.Close())
			binds[index].Parent = nil
		}
	}

	return closeErr
}

func openNativeRuntimeRoot(
	directories *bwrap.RunDirectories,
	uid, gid int,
	logger *diagnostic.Logger,
) (*safefs.Directory, error) {
	if directories == nil {
		return nil, fmt.Errorf("open native runtime root: run directories are nil")
	}

	file, err := directories.RuntimeFile()
	if err != nil {
		return nil, fmt.Errorf("open native runtime root descriptor: %w", err)
	}
	root, err := safefs.OpenDirectoryFile(
		file,
		directories.RuntimePath(),
		safefs.DirectoryOptions{
			OwnerUID: uid,
			OwnerGID: gid,
			Logger:   logger,
		},
	)
	logger.DebugError(
		"close native runtime root descriptor",
		file.Close(),
	)
	if err != nil {
		return nil, err
	}

	return root, nil
}

func openSandboxBinary() (string, *os.File, error) {
	path, err := executable.Resolve(executable.Sandbox)
	if err != nil {
		return "", nil, fmt.Errorf("resolve sandbox helper: %w", err)
	}

	file, err := os.Open(path)
	if err != nil {
		return "", nil, fmt.Errorf("open sandbox helper: %w", err)
	}

	return path, file, nil
}
