//! HTTPS downloads. Toby downloads exactly one thing itself: the Debian cloud
//! image used once to bootstrap the default image.

use std::io::{self, Read, Write};
use std::path::Path;

use sha2::{Digest, Sha512};

fn get(url: &str) -> io::Result<ureq::http::Response<ureq::Body>> {
    ureq::get(url).call().map_err(|e| io::Error::other(format!("{url}: {e}")))
}

pub fn text(url: &str) -> io::Result<String> {
    get(url)?.body_mut().read_to_string().map_err(|e| io::Error::other(format!("{url}: {e}")))
}

/// Downloads `url` to `path` and returns the SHA-512 of the content (hex).
pub fn file(url: &str, path: &Path) -> io::Result<String> {
    let mut resp = get(url)?;
    let mut reader = resp.body_mut().with_config().limit(u64::MAX).reader();
    let mut f = std::fs::File::create(path)?;
    let mut h = Sha512::new();
    let mut buf = vec![0u8; 256 * 1024];
    loop {
        let n = reader.read(&mut buf)?;
        if n == 0 {
            break;
        }
        h.update(&buf[..n]);
        f.write_all(&buf[..n])?;
    }
    f.sync_all()?;
    Ok(h.finalize().iter().map(|b| format!("{b:02x}")).collect())
}
