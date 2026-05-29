package fusekit

import "testing"

func TestInodeTableReturnsStableInodeForCachedKey(t *testing.T) {
	table := NewInodeTable(8)
	key := ObjectKey{MountID: "m", Kind: "file", Key: "1"}
	first := table.Access(key)
	second := table.Access(key)
	if first != second {
		t.Fatalf("same key got inodes %d and %d", first, second)
	}
	other := table.Access(ObjectKey{MountID: "m", Kind: "file", Key: "2"})
	if other == first {
		t.Fatalf("different keys got same inode %d", first)
	}
}

func TestInodeTableEvictsInactiveEntriesAndDoesNotReuseInodes(t *testing.T) {
	table := NewInodeTable(2)
	firstKey := ObjectKey{MountID: "m", Kind: "file", Key: "1"}
	first := table.Access(firstKey)
	_ = table.Access(ObjectKey{MountID: "m", Kind: "file", Key: "2"})
	_ = table.Access(ObjectKey{MountID: "m", Kind: "file", Key: "3"})
	if table.Len() != 2 {
		t.Fatalf("table len = %d, want 2", table.Len())
	}
	recreated := table.Access(firstKey)
	if recreated == first {
		t.Fatalf("evicted inode was reused: %d", first)
	}
}

func TestInodeTablePinnedEntriesAreNotEvicted(t *testing.T) {
	table := NewInodeTable(1)
	key := ObjectKey{MountID: "m", Kind: "file", Key: "pinned"}
	pin := table.Pin(key)
	_ = table.Access(ObjectKey{MountID: "m", Kind: "file", Key: "other"})
	if table.ActiveLen() != 1 {
		t.Fatalf("active len = %d, want 1", table.ActiveLen())
	}
	if got := table.Access(key); got != pin.Inode {
		t.Fatalf("pinned inode changed from %d to %d", pin.Inode, got)
	}
	pin.Unpin()
	if table.ActiveLen() != 0 {
		t.Fatalf("active len after unpin = %d, want 0", table.ActiveLen())
	}
}
