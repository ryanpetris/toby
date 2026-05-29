# fusekit

`fusekit` is a small virtual filesystem toolkit built on `github.com/hanwen/go-fuse/v2/fs`. It routes operations through ordered mounts, assigns stable virtual inode numbers, exposes synthetic parent directories, and provides passthrough and empty-directory mount types.

## Virtual Paths

All paths inside `fusekit` are virtual paths rooted at the FUSE mount point.

Rules:

- Paths are absolute.
- Paths are cleaned with `.` and `..` removed.
- Paths always start with `/`.
- `/` is the only path with a trailing slash.
- Mount base paths are normalized when the router publishes a mount list.

Host or backing paths are only used by concrete mounts such as `PassthroughMount`.

## Mount Ordering

Mounts are stored from low priority to high priority. Later mounts override earlier mounts for matching paths.

A mount matches an operation when the operation path equals the mount base path or is below it. The router builds a highest-to-lowest priority chain for each operation and calls the first mount.

## Mount Interface

```go
type Mount interface {
    ID() string
    BasePath() string
    Handle(context.Context, Operation, Next) (Result, error)
}

type Next func(context.Context, Operation) (Result, error)
```

Mounts can handle an operation, return an error, or call `next` to pass the operation to the next lower-priority matching mount.

Mounts can optionally implement `CrossMountRenamePreparer` to prepare a destination before the router returns `EXDEV` for a cross-mount rename. Returning `true` tells the router to invalidate the destination entry before returning `EXDEV`.

`MountAdapter` is a convenience helper with optional operation handlers. It is useful in tests and for small custom mounts that only need to override one or two operations.

## Operations

The router supports path-based operations including `GetAttr`, `ReadDir`, `Open`, `Create`, `Mkdir`, `Unlink`, `Rmdir`, `Readlink`, `Symlink`, `SetAttr`, `Materialize`, and `Rename`.

Errors are returned as `syscall.Errno` values where possible. Use `ErrnoOf(err)` when adapting router errors to FUSE errno returns.

## Rename

Rename has coordinator-level cross-mount behavior:

- The router resolves the top matching mount for the old path.
- The router resolves the top matching mount for the new path.
- If either side has no matching mount, rename returns `ENOENT`.
- If the top mounts differ, the destination mount can prepare for copy fallback through `CrossMountRenamePreparer`, then rename returns `EXDEV`.
- If the top mounts are the same, the rename operation is sent to that mount.

The router does not implement copy-and-delete fallback.

## Synthetic Directories

The router derives synthetic parent directories from higher-priority mount base paths.

Example: if a mount exists at `/foo/bar/baz`, then `/foo` and `/foo/bar` can be exposed as synthetic directories when no lower-priority mount provides real entries for those paths.

`ReadDir` merges synthetic entries after reading real entries from the handling mount. Real entries win when a real entry and a synthetic entry have the same name.

`GetAttr` returns directory metadata for synthetic directories. Synthetic directory object keys are path-based and stay stable while cached.

## Materialization

Create-like operations below a synthetic parent cause the router to materialize that parent in the lower-priority mount that owns the area.

Materialization is attempted for `Create`, `Open` with create flags, `Mkdir`, `Symlink`, and rename destinations. Read operations do not materialize synthetic directories.

`PassthroughMount` materializes by creating directories in its backing source with `os.MkdirAll`. Read-only passthrough mounts return `EROFS`.

## Inodes

`fusekit` uses `ObjectKey` values for object identity:

```go
type ObjectKey struct {
    MountID string
    Kind    string
    Key     string
}
```

The inode table maps object keys to virtual inode numbers. It uses two tiers:

- Active entries are pinned and not evictable.
- Inactive entries are kept in a deterministic LRU.

Inactive entries are evicted from the back of the LRU when capacity is exceeded. Virtual inode numbers are not reused during a router lifetime.

Open handles returned by the router pin their object key until release. This keeps active object identity stable across mount table updates.

## Invalidation

Invalidation is best-effort and hidden behind a small interface:

```go
type Invalidator interface {
    EntryChanged(parentPath, name string)
    InodeChanged(key ObjectKey)
}
```

Implementations include:

- `NoopInvalidator` for tests and non-mounted use.
- `RecordingInvalidator` for unit tests.
- A go-fuse invalidator used by `MountServer`.

The go-fuse invalidator notifies known parent entries and known inode content. If the adapter does not know a node, it does nothing and relies on near-zero TTLs.

## EmptyDirMount

`EmptyDirMount` exposes an empty read-only directory at its base path. It returns no real children. Router-generated synthetic children can still appear in directory listings.

Mutating operations return `EROFS`.

## PassthroughMount

`PassthroughMount` maps a virtual base path to a host source path:

```go
type PassthroughOptions struct {
    ID       string
    BasePath string
    Source   string
    ReadOnly bool
}
```

It translates virtual paths below `BasePath` to paths below `Source`. Host sources are not pre-checked when the mount is created. Missing paths return `ENOENT` at operation time.

Supported operations include metadata lookup, directory listing, open, create, mkdir, unlink, rmdir, rename within the same mount, readlink, symlink, basic setattr, and directory materialization.

Read-only passthrough mounts return `EROFS` for mutating operations and write opens.

## go-fuse Adapter

`MountServer(ctx, mountpoint, router)` mounts a router with short entry, attribute, and negative-entry TTLs. It returns a server with `Unmount` and `Wait` methods.

The adapter converts go-fuse node and file-handle calls into router operations and wraps `fusekit` file handles for read, write, flush, fsync, and release.

## Toby Integration

Toby builds one router per run and binds the FUSE mount point at `$HOME` inside bubblewrap.

The Toby mount list is ordered as:

- `/` as a writable passthrough to the private Toby home directory.
- HOME-visible tool binds as passthrough mounts.
- The project directory as a passthrough mount when a host project is used.

Tool bind paths inside bubblewrap must be `$HOME` or below `$HOME`; their host sources can be anywhere. Device-oriented binds remain bubblewrap-managed.

Projects must resolve to `$HOME` or below `$HOME` so they can be represented in the HOME-rooted virtual tree.
