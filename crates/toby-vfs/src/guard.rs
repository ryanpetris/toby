//! Policy wrapper around a fuse-backend-rs file system: Toby's identity
//! mapping (plan §10.3), read-only enforcement, and session restarts.
use std::any::Any;
use std::ffi::CStr;
use std::io;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, RwLock};
use std::time::Duration;

use fuse_backend_rs::abi::fuse_abi::{CreateIn, stat64, statvfs64};
use fuse_backend_rs::abi::virtio_fs::RemovemappingOne;
use fuse_backend_rs::api::BackendFileSystem;
use fuse_backend_rs::api::filesystem::{
    Context, DirEntry, Entry, FileLock, FileSystem, FsOptions, GetxattrReply, IoctlData, ListxattrReply,
    OpenOptions, SetattrValid, ZeroCopyReader, ZeroCopyWriter,
};
use fuse_backend_rs::transport::FsCacheReqHandler;

/// Owner reported for files not owned by the host user.
pub const OVERFLOW_ID: u32 = 65534;

/// Maps the host user to the guest user and every other owner to
/// [`OVERFLOW_ID`]; all operations run as the host user.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Squash {
    pub host_uid: u32,
    pub host_gid: u32,
    pub guest_uid: u32,
    pub guest_gid: u32,
}

/// Replaces the inner file system for a new session. It receives the slot so
/// the caller can swap it while holding its own locks.
pub type Rebuild<F> = Box<dyn Fn(&RwLock<Arc<F>>) -> io::Result<()> + Send + Sync>;

/// Wraps a file system with Toby's policies.
pub struct Guard<F> {
    inner: RwLock<Arc<F>>,
    squash: Option<Squash>,
    read_only: bool,
    /// Builds a fresh inner file system when a new FUSE session starts.
    rebuild: Option<Rebuild<F>>,
    initialized: AtomicBool,
}

fn err(code: i32) -> io::Error {
    io::Error::from_raw_os_error(code)
}

const WRITE_FLAGS: u32 =
    (libc::O_WRONLY | libc::O_RDWR | libc::O_TRUNC | libc::O_APPEND | libc::O_CREAT) as u32;
const SETID: u32 = libc::S_ISUID | libc::S_ISGID;

impl<F: FileSystem> Guard<F> {
    /// The top-level wrapper: identity mapping, and `rebuild` provides a
    /// fresh file system for every FUSE session after the first.
    pub fn top(inner: F, squash: Squash, rebuild: Rebuild<F>) -> Self {
        Guard {
            inner: RwLock::new(Arc::new(inner)),
            squash: Some(squash),
            read_only: false,
            rebuild: Some(rebuild),
            initialized: AtomicBool::new(false),
        }
    }

    /// Refuses every modification with `EROFS`.
    pub fn read_only(inner: F) -> Self {
        Guard {
            inner: RwLock::new(Arc::new(inner)),
            squash: None,
            read_only: true,
            rebuild: None,
            initialized: AtomicBool::new(false),
        }
    }

    /// The current inner file system.
    pub fn current(&self) -> Arc<F> {
        self.inner.read().unwrap().clone()
    }

    fn map_attr(&self, st: &mut stat64) {
        if let Some(s) = self.squash {
            st.st_uid = if st.st_uid == s.host_uid { s.guest_uid } else { OVERFLOW_ID };
            st.st_gid = if st.st_gid == s.host_gid { s.guest_gid } else { OVERFLOW_ID };
        }
    }

    fn map_entry(&self, mut e: Entry) -> Entry {
        self.map_attr(&mut e.attr);
        e
    }

    fn writable(&self) -> io::Result<()> {
        if self.read_only { Err(err(libc::EROFS)) } else { Ok(()) }
    }

    fn check_name(name: &CStr) -> io::Result<()> {
        match name.to_bytes() {
            b"." | b".." => Err(err(libc::ENOENT)),
            _ => Ok(()),
        }
    }
}

impl<F: BackendFileSystem<Inode = u64, Handle = u64> + 'static> BackendFileSystem for Guard<F> {
    fn mount(&self) -> io::Result<(Entry, u64)> {
        self.current().mount().map(|(e, max)| (self.map_entry(e), max))
    }

    fn as_any(&self) -> &dyn Any {
        self
    }
}

impl<F: FileSystem> FileSystem for Guard<F> {
    type Inode = F::Inode;
    type Handle = F::Handle;

    fn init(&self, capable: FsOptions) -> io::Result<FsOptions> {
        // A new FUSE session (the kernel after the firmware's own session, or
        // after a guest reboot) starts with INIT and no DESTROY. The previous
        // session's negotiated options must not carry over, so it gets a
        // freshly built file system.
        if self.initialized.swap(true, Ordering::AcqRel) {
            let old = self.current();
            old.destroy();
            if let Some(rebuild) = &self.rebuild {
                rebuild(&self.inner)?;
            }
        }
        self.current().init(capable)
    }

    fn destroy(&self) {
        // The session stays marked as used, so the next INIT (after a guest
        // reboot) starts on a freshly built file system.
        self.current().destroy()
    }

    fn id_remap(&self, ctx: &mut Context) -> io::Result<()> {
        if self.squash.is_some() {
            // Every operation runs as the (unprivileged) host user.
            ctx.uid = 0;
            ctx.gid = 0;
        }
        self.current().id_remap(ctx)
    }

    fn lookup(&self, ctx: &Context, parent: Self::Inode, name: &CStr) -> io::Result<Entry> {
        Self::check_name(name)?;
        self.current().lookup(ctx, parent, name).map(|e| self.map_entry(e))
    }

    fn forget(&self, ctx: &Context, inode: Self::Inode, count: u64) {
        self.current().forget(ctx, inode, count)
    }

    fn batch_forget(&self, ctx: &Context, requests: Vec<(Self::Inode, u64)>) {
        self.current().batch_forget(ctx, requests)
    }

    fn getattr(
        &self,
        ctx: &Context,
        inode: Self::Inode,
        handle: Option<Self::Handle>,
    ) -> io::Result<(stat64, Duration)> {
        self.current().getattr(ctx, inode, handle).map(|(mut st, d)| {
            self.map_attr(&mut st);
            (st, d)
        })
    }

    fn setattr(
        &self,
        ctx: &Context,
        inode: Self::Inode,
        mut attr: stat64,
        handle: Option<Self::Handle>,
        mut valid: SetattrValid,
    ) -> io::Result<(stat64, Duration)> {
        self.writable()?;
        if let Some(s) = self.squash {
            if valid.contains(SetattrValid::UID) {
                if attr.st_uid != s.guest_uid && attr.st_uid != 0 {
                    return Err(err(libc::EPERM));
                }
                valid.remove(SetattrValid::UID);
            }
            if valid.contains(SetattrValid::GID) {
                if attr.st_gid != s.guest_gid && attr.st_gid != 0 {
                    return Err(err(libc::EPERM));
                }
                valid.remove(SetattrValid::GID);
            }
        }
        if valid.contains(SetattrValid::MODE) {
            attr.st_mode &= !SETID;
        }
        let res = if valid.is_empty() {
            self.current().getattr(ctx, inode, handle)
        } else {
            self.current().setattr(ctx, inode, attr, handle, valid)
        };
        res.map(|(mut st, d)| {
            self.map_attr(&mut st);
            (st, d)
        })
    }

    fn readlink(&self, ctx: &Context, inode: Self::Inode) -> io::Result<Vec<u8>> {
        self.current().readlink(ctx, inode)
    }

    fn symlink(&self, ctx: &Context, linkname: &CStr, parent: Self::Inode, name: &CStr) -> io::Result<Entry> {
        self.writable()?;
        Self::check_name(name)?;
        self.current().symlink(ctx, linkname, parent, name).map(|e| self.map_entry(e))
    }

    fn mknod(
        &self,
        ctx: &Context,
        inode: Self::Inode,
        name: &CStr,
        mode: u32,
        rdev: u32,
        umask: u32,
    ) -> io::Result<Entry> {
        self.writable()?;
        Self::check_name(name)?;
        match mode & libc::S_IFMT {
            libc::S_IFREG | libc::S_IFSOCK => {}
            _ => return Err(err(libc::EPERM)),
        }
        self.current().mknod(ctx, inode, name, mode & !SETID, rdev, umask).map(|e| self.map_entry(e))
    }

    fn mkdir(
        &self,
        ctx: &Context,
        parent: Self::Inode,
        name: &CStr,
        mode: u32,
        umask: u32,
    ) -> io::Result<Entry> {
        self.writable()?;
        Self::check_name(name)?;
        self.current().mkdir(ctx, parent, name, mode & !SETID, umask).map(|e| self.map_entry(e))
    }

    fn unlink(&self, ctx: &Context, parent: Self::Inode, name: &CStr) -> io::Result<()> {
        self.writable()?;
        Self::check_name(name)?;
        self.current().unlink(ctx, parent, name)
    }

    fn rmdir(&self, ctx: &Context, parent: Self::Inode, name: &CStr) -> io::Result<()> {
        self.writable()?;
        Self::check_name(name)?;
        self.current().rmdir(ctx, parent, name)
    }

    fn rename(
        &self,
        ctx: &Context,
        olddir: Self::Inode,
        oldname: &CStr,
        newdir: Self::Inode,
        newname: &CStr,
        flags: u32,
    ) -> io::Result<()> {
        self.writable()?;
        Self::check_name(oldname)?;
        Self::check_name(newname)?;
        self.current().rename(ctx, olddir, oldname, newdir, newname, flags)
    }

    fn link(
        &self,
        ctx: &Context,
        inode: Self::Inode,
        newparent: Self::Inode,
        newname: &CStr,
    ) -> io::Result<Entry> {
        self.writable()?;
        Self::check_name(newname)?;
        self.current().link(ctx, inode, newparent, newname).map(|e| self.map_entry(e))
    }

    fn open(
        &self,
        ctx: &Context,
        inode: Self::Inode,
        flags: u32,
        fuse_flags: u32,
    ) -> io::Result<(Option<Self::Handle>, OpenOptions, Option<u32>)> {
        if flags & WRITE_FLAGS != 0 {
            self.writable()?;
        }
        self.current().open(ctx, inode, flags, fuse_flags)
    }

    fn create(
        &self,
        ctx: &Context,
        parent: Self::Inode,
        name: &CStr,
        mut args: CreateIn,
    ) -> io::Result<(Entry, Option<Self::Handle>, OpenOptions, Option<u32>)> {
        self.writable()?;
        Self::check_name(name)?;
        args.mode &= !SETID;
        self.current().create(ctx, parent, name, args).map(|(e, h, o, x)| (self.map_entry(e), h, o, x))
    }

    fn read(
        &self,
        ctx: &Context,
        inode: Self::Inode,
        handle: Self::Handle,
        w: &mut dyn ZeroCopyWriter,
        size: u32,
        offset: u64,
        lock_owner: Option<u64>,
        flags: u32,
    ) -> io::Result<usize> {
        self.current().read(ctx, inode, handle, w, size, offset, lock_owner, flags)
    }

    fn write(
        &self,
        ctx: &Context,
        inode: Self::Inode,
        handle: Self::Handle,
        r: &mut dyn ZeroCopyReader,
        size: u32,
        offset: u64,
        lock_owner: Option<u64>,
        delayed_write: bool,
        flags: u32,
        fuse_flags: u32,
    ) -> io::Result<usize> {
        self.writable()?;
        self.current().write(
            ctx,
            inode,
            handle,
            r,
            size,
            offset,
            lock_owner,
            delayed_write,
            flags,
            fuse_flags,
        )
    }

    fn flush(
        &self,
        ctx: &Context,
        inode: Self::Inode,
        handle: Self::Handle,
        lock_owner: u64,
    ) -> io::Result<()> {
        self.current().flush(ctx, inode, handle, lock_owner)
    }

    fn fsync(
        &self,
        ctx: &Context,
        inode: Self::Inode,
        datasync: bool,
        handle: Self::Handle,
    ) -> io::Result<()> {
        self.current().fsync(ctx, inode, datasync, handle)
    }

    fn fallocate(
        &self,
        ctx: &Context,
        inode: Self::Inode,
        handle: Self::Handle,
        mode: u32,
        offset: u64,
        length: u64,
    ) -> io::Result<()> {
        self.writable()?;
        self.current().fallocate(ctx, inode, handle, mode, offset, length)
    }

    fn release(
        &self,
        ctx: &Context,
        inode: Self::Inode,
        flags: u32,
        handle: Self::Handle,
        flush: bool,
        flock_release: bool,
        lock_owner: Option<u64>,
    ) -> io::Result<()> {
        self.current().release(ctx, inode, flags, handle, flush, flock_release, lock_owner)
    }

    fn statfs(&self, ctx: &Context, inode: Self::Inode) -> io::Result<statvfs64> {
        self.current().statfs(ctx, inode)
    }

    fn setxattr(
        &self,
        ctx: &Context,
        inode: Self::Inode,
        name: &CStr,
        value: &[u8],
        flags: u32,
    ) -> io::Result<()> {
        self.writable()?;
        self.current().setxattr(ctx, inode, name, value, flags)
    }

    fn getxattr(
        &self,
        ctx: &Context,
        inode: Self::Inode,
        name: &CStr,
        size: u32,
    ) -> io::Result<GetxattrReply> {
        self.current().getxattr(ctx, inode, name, size)
    }

    fn listxattr(&self, ctx: &Context, inode: Self::Inode, size: u32) -> io::Result<ListxattrReply> {
        self.current().listxattr(ctx, inode, size)
    }

    fn removexattr(&self, ctx: &Context, inode: Self::Inode, name: &CStr) -> io::Result<()> {
        self.writable()?;
        self.current().removexattr(ctx, inode, name)
    }

    fn opendir(
        &self,
        ctx: &Context,
        inode: Self::Inode,
        flags: u32,
    ) -> io::Result<(Option<Self::Handle>, OpenOptions)> {
        self.current().opendir(ctx, inode, flags)
    }

    fn readdir(
        &self,
        ctx: &Context,
        inode: Self::Inode,
        handle: Self::Handle,
        size: u32,
        offset: u64,
        add_entry: &mut dyn FnMut(DirEntry) -> io::Result<usize>,
    ) -> io::Result<()> {
        self.current().readdir(ctx, inode, handle, size, offset, add_entry)
    }

    fn readdirplus(
        &self,
        ctx: &Context,
        inode: Self::Inode,
        handle: Self::Handle,
        size: u32,
        offset: u64,
        add_entry: &mut dyn FnMut(DirEntry, Entry) -> io::Result<usize>,
    ) -> io::Result<()> {
        self.current()
            .readdirplus(ctx, inode, handle, size, offset, &mut |d, e| add_entry(d, self.map_entry(e)))
    }

    fn fsyncdir(
        &self,
        ctx: &Context,
        inode: Self::Inode,
        datasync: bool,
        handle: Self::Handle,
    ) -> io::Result<()> {
        self.current().fsyncdir(ctx, inode, datasync, handle)
    }

    fn releasedir(
        &self,
        ctx: &Context,
        inode: Self::Inode,
        flags: u32,
        handle: Self::Handle,
    ) -> io::Result<()> {
        self.current().releasedir(ctx, inode, flags, handle)
    }

    fn setupmapping(
        &self,
        _ctx: &Context,
        _inode: Self::Inode,
        _handle: Self::Handle,
        _foffset: u64,
        _len: u64,
        _flags: u64,
        _moffset: u64,
        _vu_req: &mut dyn FsCacheReqHandler,
    ) -> io::Result<()> {
        Err(err(libc::ENOSYS))
    }

    fn removemapping(
        &self,
        _ctx: &Context,
        _inode: Self::Inode,
        _requests: Vec<RemovemappingOne>,
        _vu_req: &mut dyn FsCacheReqHandler,
    ) -> io::Result<()> {
        Err(err(libc::ENOSYS))
    }

    fn access(&self, ctx: &Context, inode: Self::Inode, mask: u32) -> io::Result<()> {
        if mask & libc::W_OK as u32 != 0 {
            self.writable()?;
        }
        self.current().access(ctx, inode, mask)
    }

    fn lseek(
        &self,
        ctx: &Context,
        inode: Self::Inode,
        handle: Self::Handle,
        offset: u64,
        whence: u32,
    ) -> io::Result<u64> {
        self.current().lseek(ctx, inode, handle, offset, whence)
    }

    fn getlk(
        &self,
        ctx: &Context,
        inode: Self::Inode,
        handle: Self::Handle,
        owner: u64,
        lock: FileLock,
        flags: u32,
    ) -> io::Result<FileLock> {
        self.current().getlk(ctx, inode, handle, owner, lock, flags)
    }

    fn setlk(
        &self,
        ctx: &Context,
        inode: Self::Inode,
        handle: Self::Handle,
        owner: u64,
        lock: FileLock,
        flags: u32,
    ) -> io::Result<()> {
        self.current().setlk(ctx, inode, handle, owner, lock, flags)
    }

    fn setlkw(
        &self,
        ctx: &Context,
        inode: Self::Inode,
        handle: Self::Handle,
        owner: u64,
        lock: FileLock,
        flags: u32,
    ) -> io::Result<()> {
        self.current().setlkw(ctx, inode, handle, owner, lock, flags)
    }

    fn ioctl(
        &self,
        _ctx: &Context,
        _inode: Self::Inode,
        _handle: Self::Handle,
        _flags: u32,
        _cmd: u32,
        _data: IoctlData,
        _out_size: u32,
    ) -> io::Result<IoctlData<'_>> {
        Err(err(libc::ENOTTY))
    }

    fn bmap(&self, _ctx: &Context, _inode: Self::Inode, _block: u64, _blocksize: u32) -> io::Result<u64> {
        Err(err(libc::ENOSYS))
    }

    fn poll(
        &self,
        ctx: &Context,
        inode: Self::Inode,
        handle: Self::Handle,
        khandle: Self::Handle,
        flags: u32,
        events: u32,
    ) -> io::Result<u32> {
        self.current().poll(ctx, inode, handle, khandle, flags, events)
    }

    fn notify_reply(&self) -> io::Result<()> {
        self.current().notify_reply()
    }
}
