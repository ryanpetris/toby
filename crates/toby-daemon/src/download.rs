//! HTTPS downloads. Toby downloads exactly one thing itself: the Debian cloud
//! image used once to bootstrap the default image.

use std::io::{self, Read, Write};
use std::path::Path;
use std::time::Duration;

use sha2::{Digest, Sha512};

fn get(url: &str) -> io::Result<ureq::http::Response<ureq::Body>> {
    let agent: ureq::Agent = ureq::Agent::config_builder()
        .timeout_connect(Some(Duration::from_secs(30)))
        .timeout_recv_response(Some(Duration::from_secs(60)))
        .timeout_recv_body(Some(Duration::from_secs(3600)))
        .build()
        .into();
    agent.get(url).call().map_err(|e| io::Error::other(format!("{url}: {e}")))
}

pub fn text(url: &str) -> io::Result<String> {
    get(url)?.body_mut().read_to_string().map_err(|e| io::Error::other(format!("{url}: {e}")))
}

/// Downloads `url` to `path`, telling `progress` the bytes so far and the
/// size if known, and returns the SHA-512 of the content (hex).
pub fn file(url: &str, path: &Path, progress: &mut dyn FnMut(u64, Option<u64>)) -> io::Result<String> {
    let mut resp = get(url)?;
    let total = resp.headers().get("content-length").and_then(|v| v.to_str().ok()?.parse::<u64>().ok());
    let mut done = 0u64;
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
        done += n as u64;
        progress(done, total);
    }
    f.sync_all()?;
    Ok(h.finalize().iter().map(|b| format!("{b:02x}")).collect())
}
