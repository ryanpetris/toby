//go:build linux

package resourcelog

// Creates and reopens bounded agent resource logs through retained directory
// capabilities without exposing resource configuration.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"

	"petris.dev/toby/internal/agent/protocol"
	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/storage/safefs"
)

const retainedLogsPerResource = 32

// Service opens append-only logs beneath the current user's Toby cache root.
type Service struct {
	paths  config.Paths
	uid    int
	gid    int
	logger *diagnostic.Logger
}

// NewService constructs a dormant resource-log service.
func NewService(
	paths config.Paths,
	diagnostics *diagnostic.Service,
) *Service {
	return &Service{
		paths:  paths,
		uid:    os.Geteuid(),
		gid:    os.Getegid(),
		logger: diagnostics.Logger("agent.resource-log"),
	}
}

// Create creates one owner-only operation or generation log and applies
// bounded per-resource retention.
func (s *Service) Create(
	kind protocol.ResourceKind,
	resourceID protocol.ResourceID,
	operationID protocol.OperationID,
) (result *os.File, returnErr error) {
	directory, err := s.openResourceDirectory(kind, resourceID, true)
	if err != nil {
		return nil, err
	}
	defer func() {
		s.logger.DebugError(
			"close resource log directory",
			directory.Close(),
		)
	}()

	parent, err := directory.File()
	if err != nil {
		return nil, fmt.Errorf("duplicate resource log directory: %w", err)
	}
	defer func() {
		s.logger.DebugError(
			"close resource log directory descriptor",
			parent.Close(),
		)
	}()

	name, err := operationLogName(operationID)
	if err != nil {
		return nil, err
	}
	fd, err := unix.Openat(
		int(parent.Fd()),
		name,
		unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|
			unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0o600,
	)
	if errors.Is(err, unix.EEXIST) {
		return nil, fmt.Errorf(
			"generated duplicate resource log operation ID %q",
			operationID,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("create resource log %q: %w", name, err)
	}

	file := os.NewFile(
		uintptr(fd),
		filepath.Join(directory.Path(), name),
	)
	if err := retainNewestLogs(parent, name); err != nil {
		s.logger.DebugError(
			"retain bounded resource logs",
			err,
			"resource_id",
			resourceID,
		)
	}

	return file, nil
}

// Open opens one requested or latest retained resource log and returns its
// source operation identity.
func (s *Service) Open(
	kind protocol.ResourceKind,
	resourceID protocol.ResourceID,
	operationID protocol.OperationID,
) (
	result *os.File,
	resultOperationID protocol.OperationID,
	returnErr error,
) {
	directory, err := s.openResourceDirectory(kind, resourceID, false)
	if err != nil {
		return nil, "", err
	}
	defer func() {
		s.logger.DebugError(
			"close resource log directory",
			directory.Close(),
		)
	}()

	parent, err := directory.File()
	if err != nil {
		return nil, "", fmt.Errorf(
			"duplicate resource log directory: %w",
			err,
		)
	}
	defer func() {
		s.logger.DebugError(
			"close resource log directory descriptor",
			parent.Close(),
		)
	}()

	name := ""
	if operationID != "" {
		name, err = operationLogName(operationID)
	} else {
		name, operationID, err = latestLog(parent)
	}
	if err != nil {
		return nil, "", err
	}

	fd, err := unix.Openat(
		int(parent.Fd()),
		name,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return nil, "", fmt.Errorf("open resource log %q: %w", name, err)
	}
	if err := s.validateLogFile(fd, name); err != nil {
		closeDescriptor(s.logger, fd)
		return nil, "", err
	}

	return os.NewFile(
		uintptr(fd),
		filepath.Join(directory.Path(), name),
	), operationID, nil
}

func (s *Service) openResourceDirectory(
	kind protocol.ResourceKind,
	resourceID protocol.ResourceID,
	create bool,
) (*safefs.Directory, error) {
	if s == nil {
		return nil, fmt.Errorf("resource log service is nil")
	}
	if err := kind.Validate(); err != nil {
		return nil, err
	}
	id, err := resourceDirectoryName(resourceID)
	if err != nil {
		return nil, err
	}

	var cacheRoot *safefs.Directory
	if create {
		cacheRoot, err = safefs.OpenOrCreateRoot(
			s.paths.TobyCacheDir(),
			s.directoryOptions(),
		)
	} else {
		cacheRoot, err = safefs.OpenPrivateRoot(
			s.paths.TobyCacheDir(),
			s.directoryOptions(),
		)
	}
	if err != nil {
		return nil, fmt.Errorf("open Toby resource log cache root: %w", err)
	}

	relative := filepath.Join("logs", string(kind), id)
	var directory *safefs.Directory
	if create {
		directory, err = cacheRoot.MkdirAll(relative)
	} else {
		directory, err = cacheRoot.OpenDirectory(relative)
	}
	if err != nil {
		s.logger.DebugError(
			"close resource log cache root",
			cacheRoot.Close(),
		)
		return nil, fmt.Errorf("open %s log directory: %w", kind, err)
	}
	directory.RepairPrivateOwnershipAndMode()
	if err := cacheRoot.Close(); err != nil {
		s.logger.DebugError(
			"close resource log cache root",
			err,
		)
	}

	return directory, nil
}

func resourceDirectoryName(id protocol.ResourceID) (string, error) {
	value := string(id)
	if value == "" ||
		len(value) > 200 ||
		strings.ContainsAny(value, "/\x00") ||
		value == "." ||
		value == ".." {
		return "", fmt.Errorf("resource ID cannot name a log directory")
	}

	return value, nil
}

func operationLogName(id protocol.OperationID) (string, error) {
	value := string(id)
	if value == "" ||
		len(value) > 128 ||
		strings.ContainsAny(value, "/\x00") ||
		value == "." ||
		value == ".." {
		return "", fmt.Errorf("operation ID cannot name a resource log")
	}

	return value + ".jsonl", nil
}

func retainNewestLogs(parent *os.File, current string) error {
	entries, err := parent.ReadDir(-1)
	if err != nil {
		return fmt.Errorf("list resource logs: %w", err)
	}
	logs := regularLogEntries(entries)
	if len(logs) <= retainedLogsPerResource {
		return nil
	}
	sort.Slice(logs, func(i, j int) bool {
		left, leftErr := logs[i].Info()
		right, rightErr := logs[j].Info()
		if leftErr != nil || rightErr != nil ||
			left.ModTime().Equal(right.ModTime()) {
			return logs[i].Name() < logs[j].Name()
		}
		return left.ModTime().Before(right.ModTime())
	})

	remove := len(logs) - retainedLogsPerResource
	for _, entry := range logs {
		if remove == 0 {
			break
		}
		if entry.Name() == current {
			continue
		}
		if err := unix.Unlinkat(
			int(parent.Fd()),
			entry.Name(),
			0,
		); err != nil && !errors.Is(err, unix.ENOENT) {
			return fmt.Errorf(
				"remove expired resource log %q: %w",
				entry.Name(),
				err,
			)
		}
		remove--
	}

	return nil
}

func latestLog(
	parent *os.File,
) (string, protocol.OperationID, error) {
	entries, err := parent.ReadDir(-1)
	if err != nil {
		return "", "", fmt.Errorf("list resource logs: %w", err)
	}
	logs := regularLogEntries(entries)
	if len(logs) == 0 {
		return "", "", fs.ErrNotExist
	}
	sort.Slice(logs, func(i, j int) bool {
		left, leftErr := logs[i].Info()
		right, rightErr := logs[j].Info()
		if leftErr != nil || rightErr != nil ||
			left.ModTime().Equal(right.ModTime()) {
			return logs[i].Name() > logs[j].Name()
		}
		return left.ModTime().After(right.ModTime())
	})

	name := logs[0].Name()
	return name,
		protocol.OperationID(strings.TrimSuffix(name, ".jsonl")),
		nil
}

func regularLogEntries(entries []os.DirEntry) []os.DirEntry {
	result := make([]os.DirEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Type().IsRegular() &&
			strings.HasSuffix(entry.Name(), ".jsonl") {
			result = append(result, entry)
		}
	}

	return result
}

func (s *Service) validateLogFile(fd int, name string) error {
	var status unix.Stat_t
	if err := unix.Fstat(fd, &status); err != nil {
		return fmt.Errorf("inspect resource log %q: %w", name, err)
	}
	if status.Mode&unix.S_IFMT != unix.S_IFREG {
		return fmt.Errorf("resource log %q is not a regular file", name)
	}
	if int(status.Uid) != s.uid || int(status.Gid) != s.gid {
		if err := unix.Fchown(fd, s.uid, s.gid); err != nil {
			s.logger.DebugError(
				"correct resource log ownership",
				err,
				"name",
				name,
				"current_uid",
				status.Uid,
				"current_gid",
				status.Gid,
				"desired_uid",
				s.uid,
				"desired_gid",
				s.gid,
			)
		}
	}
	if status.Mode&0o777 != 0o600 {
		if err := unix.Fchmod(fd, 0o600); err != nil {
			s.logger.DebugError(
				"correct resource log mode",
				err,
				"name",
				name,
				"current_mode",
				fmt.Sprintf("%#o", status.Mode&0o777),
				"desired_mode",
				"0600",
			)
		}
	}

	return nil
}

func (s *Service) directoryOptions() safefs.DirectoryOptions {
	return safefs.DirectoryOptions{
		OwnerUID: s.uid,
		OwnerGID: s.gid,
		Logger:   s.logger,
	}
}
