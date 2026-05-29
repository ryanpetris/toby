package fusekit

import (
	"container/list"
	"sync"
)

type ObjectKey struct {
	MountID string
	Kind    string
	Key     string
}

func (k ObjectKey) empty() bool {
	return k.MountID == "" && k.Kind == "" && k.Key == ""
}

type InodeTable struct {
	mu       sync.Mutex
	capacity int
	next     uint64
	active   map[ObjectKey]*inodeEntry
	inactive map[ObjectKey]*list.Element
	lru      *list.List
}

type inodeEntry struct {
	key   ObjectKey
	inode uint64
	refs  int
}

type InodePin struct {
	table *InodeTable
	key   ObjectKey
	Inode uint64
	once  sync.Once
}

func NewInodeTable(capacity int) *InodeTable {
	if capacity <= 0 {
		capacity = 16384
	}
	return &InodeTable{
		capacity: capacity,
		next:     2,
		active:   map[ObjectKey]*inodeEntry{},
		inactive: map[ObjectKey]*list.Element{},
		lru:      list.New(),
	}
}

func (t *InodeTable) Access(key ObjectKey) uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	if entry := t.active[key]; entry != nil {
		return entry.inode
	}
	if elem := t.inactive[key]; elem != nil {
		t.lru.MoveToFront(elem)
		return elem.Value.(*inodeEntry).inode
	}
	entry := &inodeEntry{key: key, inode: t.allocateLocked()}
	t.inactive[key] = t.lru.PushFront(entry)
	t.evictLocked()
	return entry.inode
}

func (t *InodeTable) Pin(key ObjectKey) InodePin {
	t.mu.Lock()
	defer t.mu.Unlock()
	if entry := t.active[key]; entry != nil {
		entry.refs++
		return InodePin{table: t, key: key, Inode: entry.inode}
	}
	if elem := t.inactive[key]; elem != nil {
		entry := elem.Value.(*inodeEntry)
		t.lru.Remove(elem)
		delete(t.inactive, key)
		entry.refs = 1
		t.active[key] = entry
		return InodePin{table: t, key: key, Inode: entry.inode}
	}
	entry := &inodeEntry{key: key, inode: t.allocateLocked(), refs: 1}
	t.active[key] = entry
	return InodePin{table: t, key: key, Inode: entry.inode}
}

func (p *InodePin) Unpin() {
	if p == nil || p.table == nil {
		return
	}
	p.once.Do(func() { p.table.unpin(p.key) })
}

func (t *InodeTable) unpin(key ObjectKey) {
	t.mu.Lock()
	defer t.mu.Unlock()
	entry := t.active[key]
	if entry == nil {
		return
	}
	entry.refs--
	if entry.refs > 0 {
		return
	}
	delete(t.active, key)
	entry.refs = 0
	t.inactive[key] = t.lru.PushFront(entry)
	t.evictLocked()
}

func (t *InodeTable) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.active) + len(t.inactive)
}

func (t *InodeTable) ActiveLen() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.active)
}

func (t *InodeTable) InactiveLen() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.inactive)
}

func (t *InodeTable) allocateLocked() uint64 {
	inode := t.next
	t.next++
	return inode
}

func (t *InodeTable) evictLocked() {
	for len(t.active)+len(t.inactive) > t.capacity {
		back := t.lru.Back()
		if back == nil {
			return
		}
		entry := back.Value.(*inodeEntry)
		delete(t.inactive, entry.key)
		t.lru.Remove(back)
	}
}
