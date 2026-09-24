//! The control socket: attachments added and removed at runtime (plan §10.2).

use std::io;
use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};
use std::sync::Arc;

use toby_proto::fs::{self, Request, Response};
use toby_proto::{frame, types};
use toby_vfs::{MountSpec, Tree};
use tokio::net::{UnixListener, UnixStream};

/// Where attachments appear in the served tree.
pub fn attachment_path(id: &str) -> io::Result<String> {
    let ok = !id.is_empty()
        && id.len() <= 64
        && id.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'-' || b == b'_');
    if !ok {
        return Err(io::Error::new(io::ErrorKind::InvalidInput, format!("invalid attachment ID {id:?}")));
    }
    Ok(format!("/projects/{id}"))
}

fn add(tree: &Tree, add: fs::Add) -> io::Result<()> {
    let path = attachment_path(&add.id)?;
    let spec = MountSpec { source: PathBuf::from(&add.host_path), read_only: add.read_only };
    if let Some((_, current)) = tree.mounts().into_iter().find(|(p, _)| *p == path) {
        if current == spec {
            return Ok(());
        }
        return Err(io::Error::new(
            io::ErrorKind::AlreadyExists,
            format!("attachment {} already serves {}", add.id, current.source.display()),
        ));
    }
    tree.mount(&path, spec)
}

fn remove(tree: &Tree, id: &str) -> io::Result<()> {
    let path = attachment_path(id)?;
    match tree.unmount(&path) {
        Err(e) if e.kind() == io::ErrorKind::NotFound => Ok(()),
        r => r,
    }
}

fn list(tree: &Tree) -> fs::Attachments {
    let attachments = tree
        .mounts()
        .into_iter()
        .filter_map(|(path, spec)| {
            let id = path.strip_prefix("/projects/")?.to_string();
            Some(fs::Attachment {
                id,
                host_path: spec.source.to_string_lossy().into_owned(),
                read_only: spec.read_only,
            })
        })
        .collect();
    fs::Attachments { attachments }
}

async fn conn(tree: Arc<Tree>, mut s: UnixStream) -> io::Result<()> {
    let Request::Hello(hello) = frame::recv(&mut s).await? else {
        frame::send(&mut s, &Response::failed("expected hello")).await?;
        return Ok(());
    };
    let Some(version) = types::negotiate(&hello.versions) else {
        frame::send(&mut s, &Response::failed("unsupported protocol version")).await?;
        return Ok(());
    };
    frame::send(&mut s, &Response::Welcome(fs::Welcome { version })).await?;
    loop {
        let req: Request = match frame::recv(&mut s).await {
            Ok(r) => r,
            Err(toby_proto::Error::Closed) => return Ok(()),
            Err(e) => return Err(e.into()),
        };
        let done = |r: io::Result<()>| r.map_or_else(Response::failed, |()| Response::Done(fs::Done {}));
        let resp = match req {
            Request::Hello(_) => Response::failed("already greeted"),
            Request::Add(a) => done(add(&tree, a)),
            Request::Remove(r) => done(remove(&tree, &r.id)),
            Request::List(_) => Response::Attachments(list(&tree)),
        };
        frame::send(&mut s, &resp).await?;
    }
}

/// Serves the control socket on its own thread for the life of the process.
pub fn spawn(socket: &Path, tree: Arc<Tree>) -> io::Result<()> {
    let _ = std::fs::remove_file(socket);
    let std_listener = std::os::unix::net::UnixListener::bind(socket)?;
    std::fs::set_permissions(socket, std::fs::Permissions::from_mode(0o600))?;
    std_listener.set_nonblocking(true)?;
    let rt = tokio::runtime::Builder::new_current_thread().enable_all().build()?;
    std::thread::Builder::new().name("control".into()).spawn(move || {
        rt.block_on(async move {
            let Ok(listener) = UnixListener::from_std(std_listener) else { return };
            loop {
                match listener.accept().await {
                    Ok((s, _)) => {
                        tokio::spawn(conn(tree.clone(), s));
                    }
                    Err(_) => tokio::time::sleep(std::time::Duration::from_millis(100)).await,
                }
            }
        })
    })?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn attachment_ids_are_validated() {
        assert_eq!(attachment_path("a1").unwrap(), "/projects/a1");
        for bad in ["", "..", "a/b", "a b", &"x".repeat(65)] {
            assert!(attachment_path(bad).is_err(), "{bad:?}");
        }
    }

    #[tokio::test]
    async fn add_remove_and_list() {
        let dir = tempfile::tempdir().unwrap();
        let other = tempfile::tempdir().unwrap();
        let squash = toby_vfs::Squash { host_uid: 1000, host_gid: 1000, guest_uid: 1000, guest_gid: 1000 };
        let tree = Arc::new(Tree::new(squash).unwrap());
        let (mut client, server) = UnixStream::pair().unwrap();
        tokio::spawn(conn(tree.clone(), server));

        let mut call = async |req: Request| -> Response {
            frame::send(&mut client, &req).await.unwrap();
            frame::recv(&mut client).await.unwrap()
        };
        assert!(matches!(call(Request::Hello(fs::Hello { versions: vec![1] })).await, Response::Welcome(_)));
        let host = dir.path().to_string_lossy().into_owned();
        let a = fs::Add { id: "a1".into(), host_path: host.clone(), read_only: false };
        assert!(matches!(call(Request::Add(a.clone())).await, Response::Done(_)));
        // Adding the same attachment again is fine; changing it is not.
        assert!(matches!(call(Request::Add(a.clone())).await, Response::Done(_)));
        let moved = fs::Add { host_path: other.path().to_string_lossy().into_owned(), ..a.clone() };
        assert!(matches!(call(Request::Add(moved)).await, Response::Failed(_)));
        let bad = fs::Add { id: "../x".into(), ..a.clone() };
        assert!(matches!(call(Request::Add(bad)).await, Response::Failed(_)));

        match call(Request::List(fs::List {})).await {
            Response::Attachments(l) => {
                assert_eq!(l.attachments.len(), 1);
                assert_eq!(l.attachments[0].id, "a1");
            }
            other => panic!("{other:?}"),
        }
        assert!(matches!(call(Request::Remove(fs::Remove { id: "a1".into() })).await, Response::Done(_)));
        assert!(matches!(call(Request::Remove(fs::Remove { id: "a1".into() })).await, Response::Done(_)));
        assert!(tree.mounts().is_empty());
    }
}
