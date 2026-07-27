//go:build !linux

package safefs

// Reports explicit unsupported errors for Linux directory-capability APIs.

import (
	"fmt"
	"io/fs"
	"os"
)

// OpenPrivateRoot reports that secure private-root opening requires Linux.
func OpenPrivateRoot(string, DirectoryOptions) (*Directory, error) {
	return nil, unsupported("open private root")
}

// OpenOrCreateRoot reports that secure root creation requires Linux.
func OpenOrCreateRoot(string, DirectoryOptions) (*Directory, error) {
	return nil, unsupported("open or create root")
}

// OpenDirectoryFile reports that exact directory-file capabilities require
// Linux.
func OpenDirectoryFile(*os.File, string, DirectoryOptions) (*Directory, error) {
	return nil, unsupported("open directory file")
}

// Close reports that secure directory capabilities require Linux.
func (d *Directory) Close() error {
	return unsupported("close directory")
}

// File reports that secure directory capabilities require Linux.
func (d *Directory) File() (*os.File, error) {
	return nil, unsupported("duplicate directory file")
}

// Duplicate reports that secure directory capabilities require Linux.
func (d *Directory) Duplicate() (*Directory, error) {
	return nil, unsupported("duplicate directory")
}

// OpenDirectory reports that secure directory capabilities require Linux.
func (d *Directory) OpenDirectory(string) (*Directory, error) {
	return nil, unsupported("open directory")
}

// MkdirAll reports that secure directory creation requires Linux.
func (d *Directory) MkdirAll(string) (*Directory, error) {
	return nil, unsupported("create directory")
}

// Sync reports that secure directory syncing requires Linux.
func (d *Directory) Sync() error {
	return unsupported("sync directory")
}

// RepairPrivateOwnershipAndMode is unavailable off Linux.
func (d *Directory) RepairPrivateOwnershipAndMode() {
}

// ReadFile reports that secure regular-file reads require Linux.
func (d *Directory) ReadFile(string, int64) ([]byte, error) {
	return nil, unsupported("read file")
}

// OpenFile reports that secure regular-file opens require Linux.
func (d *Directory) OpenFile(string) (*os.File, error) {
	return nil, unsupported("open file")
}

// WriteFile reports that secure regular-file writes require Linux.
func (d *Directory) WriteFile(string, []byte, fs.FileMode) error {
	return unsupported("write file")
}

// PublishFile reports that no-replace file publication requires Linux.
func (d *Directory) PublishFile(string, []byte, fs.FileMode) (bool, error) {
	return false, unsupported("publish file")
}

// ReplaceFile reports that atomic file replacement requires Linux.
func (d *Directory) ReplaceFile(string, []byte, fs.FileMode) error {
	return unsupported("replace file")
}

// ReplaceFileOwned reports that owned atomic file replacement requires Linux.
func (d *Directory) ReplaceFileOwned(string, []byte, fs.FileMode, int, int) error {
	return unsupported("replace owned file")
}

// PublishDirectory reports that no-replace directory publication requires
// Linux.
func (d *Directory) PublishDirectory(string, uint64, func(*Directory) error) (bool, error) {
	return false, unsupported("publish directory")
}

// RemoveAll reports that no-follow recursive removal requires Linux.
func (d *Directory) RemoveAll(string, uint64) error {
	return unsupported("remove tree")
}

// RemoveAllOwned reports that Toby-owned recursive removal requires Linux.
func (d *Directory) RemoveAllOwned(string, uint64) error {
	return unsupported("remove owned tree")
}

// RemoveAllProgress reports that bounded no-follow removal requires Linux.
func (d *Directory) RemoveAllProgress(string, uint64) (uint64, error) {
	return 0, unsupported("remove tree")
}

// RemoveAllOwnedProgress reports that Toby-owned removal requires Linux.
func (d *Directory) RemoveAllOwnedProgress(string, uint64) (uint64, error) {
	return 0, unsupported("remove owned tree")
}

// Lock reports that secure advisory locking requires Linux.
func (d *Directory) Lock(string, LockMode, bool) (*Lock, error) {
	return nil, unsupported("lock file")
}

// LockSelf reports that secure directory locking requires Linux.
func (d *Directory) LockSelf(LockMode, bool) (*Lock, error) {
	return nil, unsupported("lock directory")
}

// Names reports that secure directory enumeration requires Linux.
func (d *Directory) Names(uint64) ([]string, error) {
	return nil, unsupported("read directory")
}

// Close reports that secure advisory locking requires Linux.
func (l *Lock) Close() error {
	return unsupported("close lock")
}

func unsupported(operation string) error {
	return fmt.Errorf("%w: %s requires Linux", ErrUnsupported, operation)
}
