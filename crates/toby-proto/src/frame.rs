//! Frame format shared by every Toby stream protocol.
//!
//! A frame is `u32 big-endian length | u8 type | CBOR payload`, where the length
//! counts the type byte and the payload. Frames are limited to [`MAX_FRAME`]
//! bytes; larger data (terminal output, replay buffers) is split by the sender.

use std::io;

use serde::Serialize;
use serde::de::DeserializeOwned;
use tokio::io::{AsyncRead, AsyncReadExt, AsyncWrite, AsyncWriteExt};

/// Largest frame accepted, including the type byte.
pub const MAX_FRAME: usize = 64 * 1024;

/// Largest data chunk a sender puts in one byte-carrying frame, leaving room
/// for the CBOR envelope.
pub const MAX_CHUNK: usize = MAX_FRAME - 1024;

#[derive(Debug, thiserror::Error)]
pub enum Error {
    #[error(transparent)]
    Io(#[from] io::Error),
    #[error("frame of {0} bytes exceeds the limit")]
    TooLarge(usize),
    #[error("empty frame")]
    Empty,
    #[error("unknown frame type {0}")]
    UnknownType(u8),
    #[error("malformed frame payload: {0}")]
    Decode(String),
    #[error("cannot encode frame: {0}")]
    Encode(String),
    #[error("connection closed")]
    Closed,
    #[error("unexpected frame type {0}")]
    Unexpected(u8),
}

impl From<Error> for io::Error {
    fn from(e: Error) -> Self {
        match e {
            Error::Io(e) => e,
            Error::Closed => io::Error::new(io::ErrorKind::UnexpectedEof, "connection closed"),
            other => io::Error::new(io::ErrorKind::InvalidData, other),
        }
    }
}

/// A raw frame: its type byte and undecoded payload.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Frame {
    pub kind: u8,
    pub payload: Vec<u8>,
}

/// Encodes one frame into a byte vector ready to write.
pub fn encode<T: Serialize>(kind: u8, msg: &T) -> Result<Vec<u8>, Error> {
    let mut buf = vec![0u8; 5];
    buf[4] = kind;
    ciborium::into_writer(msg, &mut buf).map_err(|e| Error::Encode(e.to_string()))?;

    let len = buf.len() - 4;
    if len > MAX_FRAME {
        return Err(Error::TooLarge(len));
    }
    buf[..4].copy_from_slice(&(len as u32).to_be_bytes());
    Ok(buf)
}

/// Decodes a frame payload into a message.
pub fn decode<T: DeserializeOwned>(payload: &[u8]) -> Result<T, Error> {
    ciborium::from_reader(payload).map_err(|e| Error::Decode(e.to_string()))
}

/// Reads one frame. Returns [`Error::Closed`] on a clean end of stream before
/// the first byte of a frame.
pub async fn read_frame<R: AsyncRead + Unpin>(r: &mut R) -> Result<Frame, Error> {
    let mut len = [0u8; 4];
    let mut got = 0;
    while got < 4 {
        let n = r.read(&mut len[got..]).await?;
        if n == 0 {
            return Err(if got == 0 {
                Error::Closed
            } else {
                Error::Io(io::ErrorKind::UnexpectedEof.into())
            });
        }
        got += n;
    }

    let len = u32::from_be_bytes(len) as usize;
    if len == 0 {
        return Err(Error::Empty);
    }
    if len > MAX_FRAME {
        return Err(Error::TooLarge(len));
    }

    let mut buf = vec![0u8; len];
    r.read_exact(&mut buf).await?;
    let payload = buf.split_off(1);
    Ok(Frame {
        kind: buf[0],
        payload,
    })
}

/// Writes pre-encoded frame bytes and flushes.
pub async fn write_bytes<W: AsyncWrite + Unpin>(w: &mut W, bytes: &[u8]) -> Result<(), Error> {
    w.write_all(bytes).await?;
    w.flush().await?;
    Ok(())
}

/// A set of messages that share one frame type space.
pub trait Message: Sized {
    fn kind(&self) -> u8;
    fn encode(&self) -> Result<Vec<u8>, Error>;
    fn decode(frame: &Frame) -> Result<Self, Error>;
}

/// Reads and decodes one message.
pub async fn recv<M: Message, R: AsyncRead + Unpin>(r: &mut R) -> Result<M, Error> {
    let frame = read_frame(r).await?;
    M::decode(&frame)
}

/// Encodes and writes one message.
pub async fn send<M: Message, W: AsyncWrite + Unpin>(w: &mut W, msg: &M) -> Result<(), Error> {
    write_bytes(w, &msg.encode()?).await
}

/// Declares a message enum whose variants map to fixed frame type bytes.
#[macro_export]
macro_rules! messages {
    (
        $(#[$meta:meta])*
        $vis:vis enum $name:ident {
            $( $(#[$vmeta:meta])* $kind:literal => $variant:ident($inner:ty) ),* $(,)?
        }
    ) => {
        $(#[$meta])*
        #[derive(Debug, Clone, PartialEq)]
        $vis enum $name {
            $( $(#[$vmeta])* $variant($inner) ),*
        }

        impl $crate::frame::Message for $name {
            fn kind(&self) -> u8 {
                match self { $( Self::$variant(_) => $kind ),* }
            }

            fn encode(&self) -> Result<Vec<u8>, $crate::frame::Error> {
                match self { $( Self::$variant(m) => $crate::frame::encode($kind, m) ),* }
            }

            fn decode(frame: &$crate::frame::Frame) -> Result<Self, $crate::frame::Error> {
                match frame.kind {
                    $( $kind => Ok(Self::$variant($crate::frame::decode(&frame.payload)?)), )*
                    other => Err($crate::frame::Error::UnknownType(other)),
                }
            }
        }

        $( impl From<$inner> for $name {
            fn from(m: $inner) -> Self { Self::$variant(m) }
        } )*
    };
}
