//! The per-machine virtio-fs back end: serves a [`toby_vfs::Tree`] to Cloud
//! Hypervisor over vhost-user (plan §10).

pub mod control;

use std::io;
use std::path::Path;
use std::sync::{Arc, RwLock};

use fuse_backend_rs::api::Vfs;
use fuse_backend_rs::api::server::Server;
use fuse_backend_rs::transport::{Reader, VirtioFsWriter, Writer};
use toby_vfs::Guard;
use vhost::vhost_user::Listener;
use vhost::vhost_user::message::{VhostUserProtocolFeatures, VhostUserVirtioFeatures};
use vhost_user_backend::{VhostUserBackend, VhostUserDaemon, VringMutex, VringState, VringT};
use virtio_bindings::bindings::virtio_config::VIRTIO_F_VERSION_1;
use virtio_bindings::bindings::virtio_ring::{VIRTIO_RING_F_EVENT_IDX, VIRTIO_RING_F_INDIRECT_DESC};
use virtio_queue::QueueOwnedT;
use vm_memory::{GuestAddressSpace, GuestMemoryAtomic, GuestMemoryMmap};
use vmm_sys_util::epoll::EventSet;
use vmm_sys_util::event::{EventConsumer, EventFlag, EventNotifier, new_event_consumer_and_notifier};

/// The virtio-fs tag the guest mounts.
pub const TAG: &str = "toby";

const QUEUE_SIZE: usize = 1024;
/// One high-priority queue and one request queue.
const NUM_QUEUES: usize = 2;
const TAG_LEN: usize = 36;

type Mem = GuestMemoryAtomic<GuestMemoryMmap>;

struct Backend {
    server: Server<Arc<Guard<Vfs>>>,
    mem: RwLock<Option<Mem>>,
    event_idx: RwLock<bool>,
}

fn other(e: impl std::fmt::Debug) -> io::Error {
    io::Error::other(format!("{e:?}"))
}

impl Backend {
    fn process(&self, vring: &mut VringState<Mem>) -> io::Result<()> {
        let mem =
            self.mem.read().unwrap().as_ref().ok_or_else(|| io::Error::other("no guest memory"))?.memory();
        let chains: Vec<_> = vring.get_queue_mut().iter(mem.clone()).map_err(other)?.collect();
        let event_idx = *self.event_idx.read().unwrap();

        for chain in chains {
            let head = chain.head_index();
            let reader = Reader::from_descriptor_chain(&*mem, chain.clone()).map_err(other)?;
            let writer: Writer<()> = VirtioFsWriter::new(&*mem, chain).map(Into::into).map_err(other)?;
            let len = self.server.handle_message(reader, writer, None, None).map_err(other)?;

            vring.add_used(head, len as u32).map_err(other)?;
            if !event_idx || vring.needs_notification().unwrap_or(true) {
                vring.signal_used_queue()?;
            }
        }
        Ok(())
    }
}

impl VhostUserBackend for Backend {
    type Bitmap = ();
    type Vring = VringMutex<Mem>;

    fn num_queues(&self) -> usize {
        NUM_QUEUES
    }

    fn max_queue_size(&self) -> usize {
        QUEUE_SIZE
    }

    fn features(&self) -> u64 {
        (1 << VIRTIO_F_VERSION_1)
            | (1 << VIRTIO_RING_F_INDIRECT_DESC)
            | (1 << VIRTIO_RING_F_EVENT_IDX)
            | VhostUserVirtioFeatures::PROTOCOL_FEATURES.bits()
    }

    fn protocol_features(&self) -> VhostUserProtocolFeatures {
        VhostUserProtocolFeatures::MQ
            | VhostUserProtocolFeatures::REPLY_ACK
            | VhostUserProtocolFeatures::CONFIGURE_MEM_SLOTS
            | VhostUserProtocolFeatures::CONFIG
    }

    fn get_config(&self, offset: u32, size: u32) -> Vec<u8> {
        // virtio-fs config: 36-byte NUL-padded tag, then the number of request queues.
        let mut cfg = vec![0u8; TAG_LEN + 4];
        cfg[..TAG.len()].copy_from_slice(TAG.as_bytes());
        cfg[TAG_LEN..].copy_from_slice(&1u32.to_le_bytes());
        let mut out: Vec<u8> = cfg.into_iter().skip(offset as usize).take(size as usize).collect();
        out.resize(size as usize, 0);
        out
    }

    fn set_event_idx(&self, enabled: bool) {
        *self.event_idx.write().unwrap() = enabled;
    }

    fn update_memory(&self, mem: Mem) -> io::Result<()> {
        *self.mem.write().unwrap() = Some(mem);
        Ok(())
    }

    fn handle_event(
        &self,
        device_event: u16,
        evset: EventSet,
        vrings: &[Self::Vring],
        _t: usize,
    ) -> io::Result<()> {
        if evset != EventSet::IN {
            return Err(io::Error::other("unexpected event"));
        }
        let vring = vrings.get(device_event as usize).ok_or_else(|| io::Error::other("unknown queue"))?;
        let mut state = vring.get_mut();
        if *self.event_idx.read().unwrap() {
            // With EVENT_IDX the queue must be drained until no new requests
            // arrived while notifications were off.
            loop {
                state.disable_notification().map_err(other)?;
                self.process(&mut state)?;
                if !state.enable_notification().map_err(other)? {
                    break;
                }
            }
        } else {
            self.process(&mut state)?;
        }
        Ok(())
    }

    fn exit_event(&self, _thread_index: usize) -> Option<(EventConsumer, EventNotifier)> {
        new_event_consumer_and_notifier(EventFlag::NONBLOCK).ok()
    }
}

/// Raises the open-file soft limit to the hard limit: every inode the guest
/// has looked up holds a descriptor.
pub fn raise_fd_limit() -> io::Result<u64> {
    use nix::sys::resource::{Resource, getrlimit, setrlimit};
    let (_, hard) = getrlimit(Resource::RLIMIT_NOFILE).map_err(io::Error::from)?;
    setrlimit(Resource::RLIMIT_NOFILE, hard, hard).map_err(io::Error::from)?;
    Ok(hard)
}

/// Listens on `socket` and serves `fs` to one VMM connection until it
/// disconnects. `ready` runs once the socket is listening.
pub fn serve(socket: &Path, fs: Arc<Guard<Vfs>>, ready: impl FnOnce()) -> io::Result<()> {
    let _ = std::fs::remove_file(socket);
    let mut listener = Listener::new(socket, true).map_err(other)?;
    ready();

    let backend =
        Arc::new(Backend { server: Server::new(fs), mem: RwLock::new(None), event_idx: RwLock::new(false) });
    let mut daemon =
        VhostUserDaemon::new("toby-fs".into(), backend, GuestMemoryAtomic::new(GuestMemoryMmap::new()))
            .map_err(other)?;
    daemon.start(&mut listener).map_err(other)?;
    match daemon.wait() {
        Ok(()) => Ok(()),
        Err(vhost_user_backend::Error::HandleRequest(vhost::vhost_user::Error::Disconnected)) => Ok(()),
        Err(e) => Err(other(e)),
    }
}
