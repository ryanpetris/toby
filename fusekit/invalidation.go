package fusekit

import "sync"

type Invalidator interface {
	EntryChanged(parentPath, name string)
	InodeChanged(key ObjectKey)
}

type NoopInvalidator struct{}

func (NoopInvalidator) EntryChanged(string, string) {}

func (NoopInvalidator) InodeChanged(ObjectKey) {}

type EntryInvalidation struct {
	ParentPath string
	Name       string
}

type RecordingInvalidator struct {
	mu      sync.Mutex
	Entries []EntryInvalidation
	Inodes  []ObjectKey
}

func (r *RecordingInvalidator) EntryChanged(parentPath, name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Entries = append(r.Entries, EntryInvalidation{ParentPath: parentPath, Name: name})
}

func (r *RecordingInvalidator) InodeChanged(key ObjectKey) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Inodes = append(r.Inodes, key)
}

func (r *RecordingInvalidator) EntryEvents() []EntryInvalidation {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]EntryInvalidation(nil), r.Entries...)
}

func (r *RecordingInvalidator) InodeEvents() []ObjectKey {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ObjectKey(nil), r.Inodes...)
}
