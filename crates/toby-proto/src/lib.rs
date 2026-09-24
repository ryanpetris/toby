//! Wire formats shared by Toby processes: stream headers, relay control, the session protocol, machine
//! control and file sharing control.

pub mod capability;
pub mod frame;
pub mod fs;
pub mod machine;
pub mod relay;
pub mod service;
pub mod session;
pub mod stream;
pub mod types;

pub use frame::{Error, Frame, MAX_CHUNK, MAX_FRAME, Message, recv, send};

#[cfg(test)]
mod tests {
    use super::*;
    use crate::session::{ClientFrame, ServerFrame, Stdout};
    use crate::stream::{HostHeader, Reply, SessionAttach};
    use crate::types::{ExitStatus, Identity, SpawnSpec, TtySize};

    async fn roundtrip<M: Message + PartialEq + std::fmt::Debug>(msg: M) {
        let (mut a, mut b) = tokio::io::duplex(MAX_FRAME * 2);
        send(&mut a, &msg).await.unwrap();
        let got: M = recv(&mut b).await.unwrap();
        assert_eq!(got, msg);
    }

    #[tokio::test]
    async fn messages_roundtrip() {
        roundtrip(HostHeader::SessionAttach(SessionAttach { session_id: "s1".into() })).await;
        roundtrip(Reply::version(1)).await;
        roundtrip(relay::Request::Spawn(relay::Spawn {
            spec: SpawnSpec {
                session_id: "s1".into(),
                argv: vec!["bash".into(), "-l".into()],
                env: vec![("TERM".into(), "xterm-256color".into())],
                cwd: Some("/root".into()),
                identity: Identity::Root,
                tty: Some(TtySize { rows: 24, cols: 80 }),
                keep_after_exit: true,
                start_on_attach: false,
            },
            version: Some("0.17.0".into()),
        }))
        .await;
        roundtrip(ServerFrame::Exit(session::Exit { status: ExitStatus::Signal(9) })).await;
        roundtrip(ServerFrame::Stdout(Stdout { bytes: vec![0, 1, 2, 255] })).await;
    }

    #[tokio::test]
    async fn unknown_type_is_rejected() {
        let bytes = frame::encode(99, &session::CloseStdin {}).unwrap();
        let (mut a, mut b) = tokio::io::duplex(1024);
        frame::write_bytes(&mut a, &bytes).await.unwrap();
        let err = recv::<ClientFrame, _>(&mut b).await.unwrap_err();
        assert!(matches!(err, Error::UnknownType(99)));
    }

    #[tokio::test]
    async fn oversized_frame_is_rejected_before_reading_it() {
        let (mut a, mut b) = tokio::io::duplex(1024);
        let len = (MAX_FRAME as u32 + 1).to_be_bytes();
        frame::write_bytes(&mut a, &len).await.unwrap();
        let err = frame::read_frame(&mut b).await.unwrap_err();
        assert!(matches!(err, Error::TooLarge(_)));
    }

    #[tokio::test]
    async fn clean_close_is_reported() {
        let (a, mut b) = tokio::io::duplex(16);
        drop(a);
        assert!(matches!(frame::read_frame(&mut b).await, Err(Error::Closed)));
    }

    #[test]
    fn unknown_fields_are_ignored() {
        #[derive(serde::Serialize)]
        struct Future {
            session_id: String,
            added_later: u32,
        }
        let bytes = frame::encode(2, &Future { session_id: "s".into(), added_later: 7 }).unwrap();
        let frame = Frame { kind: bytes[4], payload: bytes[5..].to_vec() };
        assert_eq!(
            HostHeader::decode(&frame).unwrap(),
            HostHeader::SessionAttach(SessionAttach { session_id: "s".into() })
        );
    }

    #[test]
    fn full_chunk_fits_in_a_frame() {
        let msg = ServerFrame::Stdout(Stdout { bytes: vec![0xff; MAX_CHUNK] });
        assert!(msg.encode().unwrap().len() - 4 <= MAX_FRAME);
    }

    #[test]
    fn negotiation_picks_newest_common_version() {
        assert_eq!(types::negotiate(&[1, 2]), Some(1));
        assert_eq!(types::negotiate(&[2]), None);
    }
}
