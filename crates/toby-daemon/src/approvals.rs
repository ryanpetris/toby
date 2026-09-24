//! Approvals (plan §16.5): host actions that wait for the user. Records are
//! files in `<state>/approvals/`, so they survive daemon restarts; the first
//! decision wins.

use std::collections::HashMap;
use std::io;
use std::path::PathBuf;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use serde::{Deserialize, Serialize};
use tokio::sync::Notify;

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Approval {
    pub id: String,
    pub created: u64,
    pub machine: String,
    /// What is asked for, such as `git.push`.
    pub kind: String,
    pub summary: String,
    pub detail: String,
    /// `pending`, `approved`, `denied` or `expired`.
    pub status: String,
    /// When a pending approval expires.
    #[serde(default)]
    pub expires: u64,
    pub decided_by: Option<String>,
    pub decided_at: Option<u64>,
}

/// Decided approvals kept for listing.
const KEEP_DECIDED: usize = 50;
/// Pending approvals one machine may have.
const MAX_PENDING: usize = 8;

/// Characters that reorder or hide text without showing themselves.
fn invisible(c: char) -> bool {
    matches!(c, '\u{200b}'..='\u{200f}' | '\u{202a}'..='\u{202e}' | '\u{2060}'..='\u{206f}' | '\u{feff}' | '\u{061c}')
}

/// Guest text for a terminal: control and invisible formatting characters
/// become spaces, and a summary is one line.
fn clean(s: &str, one_line: bool) -> String {
    s.chars()
        .map(|c| if (c.is_control() && (one_line || c != '\n')) || invisible(c) { ' ' } else { c })
        .collect()
}

pub struct Approvals {
    dir: PathBuf,
    waiting: Mutex<HashMap<String, Arc<Notify>>>,
    lock: Mutex<()>,
}

fn now() -> u64 {
    toby_store::records::now()
}

impl Approvals {
    pub fn new(dir: PathBuf) -> Approvals {
        Approvals { dir, waiting: Mutex::default(), lock: Mutex::default() }
    }

    fn path(&self, id: &str) -> PathBuf {
        self.dir.join(format!("{id}.json"))
    }

    fn load(&self, id: &str) -> io::Result<Approval> {
        if id.is_empty() || !id.bytes().all(|b| b.is_ascii_alphanumeric()) {
            return Err(io::Error::new(io::ErrorKind::InvalidInput, "invalid approval ID"));
        }
        let text = std::fs::read_to_string(self.path(id))
            .map_err(|_| io::Error::new(io::ErrorKind::NotFound, format!("no approval {id}")))?;
        serde_json::from_str(&text).map_err(io::Error::other)
    }

    fn store(&self, a: &Approval) -> io::Result<()> {
        std::fs::create_dir_all(&self.dir)?;
        let text = serde_json::to_vec_pretty(a).map_err(io::Error::other)?;
        toby_config::machine::write_atomic(&self.path(&a.id), &text)
    }

    /// Pending approvals first, then recently decided ones, newest first.
    /// Pending approvals past their time are expired.
    pub fn list(&self) -> io::Result<Vec<Approval>> {
        let mut all: Vec<Approval> = match std::fs::read_dir(&self.dir) {
            Ok(entries) => entries
                .flatten()
                .filter_map(|e| std::fs::read_to_string(e.path()).ok())
                .filter_map(|t| serde_json::from_str(&t).ok())
                .collect(),
            Err(e) if e.kind() == io::ErrorKind::NotFound => Vec::new(),
            Err(e) => return Err(e),
        };
        let now = now();
        for a in all.iter_mut().filter(|a| a.status == "pending" && a.expires <= now) {
            let _lock = self.lock.lock().unwrap();
            if let Ok(mut current) = self.load(&a.id) {
                self.expire(&mut current)?;
                *a = current;
            }
        }
        all.sort_by(|a, b| {
            (a.status != "pending").cmp(&(b.status != "pending")).then(b.created.cmp(&a.created))
        });
        Ok(all)
    }

    /// Marks a loaded approval expired if it is still pending.
    fn expire(&self, a: &mut Approval) -> io::Result<()> {
        if a.status == "pending" {
            a.status = "expired".into();
            a.decided_at = Some(now());
            self.store(a)?;
        }
        Ok(())
    }

    /// Expires every pending approval: after a restart, nothing waits for
    /// them.
    pub fn expire_all(&self) {
        let Ok(entries) = std::fs::read_dir(&self.dir) else { return };
        let _lock = self.lock.lock().unwrap();
        for e in entries.flatten() {
            let id = e.file_name().to_string_lossy().trim_end_matches(".json").to_string();
            if let Ok(mut a) = self.load(&id) {
                let _ = self.expire(&mut a);
            }
        }
    }

    pub fn create(
        &self,
        machine: &str,
        kind: &str,
        summary: String,
        detail: String,
        timeout: Duration,
    ) -> io::Result<Approval> {
        let pending = self.list()?.iter().filter(|a| a.status == "pending" && a.machine == machine).count();
        if pending >= MAX_PENDING {
            return Err(io::Error::other(format!(
                "machine {machine} already has {pending} pending approvals"
            )));
        }
        let created = now();
        let a = Approval {
            id: toby_config::new_id(),
            created,
            machine: machine.into(),
            kind: kind.into(),
            summary: clean(&summary, true),
            detail: clean(&detail, false),
            status: "pending".into(),
            expires: created + timeout.as_secs(),
            decided_by: None,
            decided_at: None,
        };
        self.store(&a)?;
        self.prune();
        Ok(a)
    }

    /// Records a decision; refused when the approval was already decided or
    /// has expired.
    pub fn decide(&self, id: &str, approve: bool, by: &str) -> io::Result<Approval> {
        let _lock = self.lock.lock().unwrap();
        let mut a = self.load(id)?;
        if a.status == "pending" && a.expires <= now() {
            self.expire(&mut a)?;
        }
        if a.status != "pending" {
            return Err(io::Error::new(
                io::ErrorKind::AlreadyExists,
                format!("approval {id} is already {}", a.status),
            ));
        }
        a.status = if approve { "approved" } else { "denied" }.into();
        a.decided_by = Some(by.into());
        a.decided_at = Some(now());
        self.store(&a)?;
        if let Some(n) = self.waiting.lock().unwrap().get(id) {
            n.notify_waiters();
        }
        Ok(a)
    }

    /// Waits for the decision until the approval expires. Returns whether
    /// it was approved; an approval left undecided, also when the waiter
    /// goes away, expires.
    pub async fn wait(&self, id: &str) -> io::Result<bool> {
        struct Waiting<'a>(&'a Approvals, &'a str);
        impl Drop for Waiting<'_> {
            fn drop(&mut self) {
                self.0.waiting.lock().unwrap().remove(self.1);
                let _lock = self.0.lock.lock().unwrap();
                if let Ok(mut a) = self.0.load(self.1) {
                    let _ = self.0.expire(&mut a);
                }
            }
        }
        let notify = self.waiting.lock().unwrap().entry(id.into()).or_default().clone();
        let _waiting = Waiting(self, id);
        let expires = self.load(id)?.expires;
        let deadline = tokio::time::Instant::now() + Duration::from_secs(expires.saturating_sub(now()));
        loop {
            let notified = notify.notified();
            match self.load(id)?.status.as_str() {
                "approved" => return Ok(true),
                "pending" => {}
                _ => return Ok(false),
            }
            if tokio::time::timeout_at(deadline, notified).await.is_err() {
                return Ok(self.load(id)?.status == "approved");
            }
        }
    }

    /// Keeps the newest decided approvals only.
    fn prune(&self) {
        let Ok(all) = self.list() else { return };
        for a in all.iter().filter(|a| a.status != "pending").skip(KEEP_DECIDED) {
            let _ = std::fs::remove_file(self.path(&a.id));
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn the_first_decision_wins() {
        let dir = tempfile::tempdir().unwrap();
        let ap = Arc::new(Approvals::new(dir.path().to_path_buf()));
        let a = ap.create("m1", "git.push", "push".into(), "detail".into(), Duration::from_secs(10)).unwrap();
        let waiter = {
            let ap = ap.clone();
            let id = a.id.clone();
            tokio::spawn(async move { ap.wait(&id).await })
        };
        tokio::time::sleep(Duration::from_millis(50)).await;
        ap.decide(&a.id, true, "cli").unwrap();
        assert!(waiter.await.unwrap().unwrap());
        assert!(ap.decide(&a.id, false, "cli").is_err());
        assert_eq!(ap.list().unwrap()[0].status, "approved");
    }

    #[tokio::test]
    async fn undecided_approvals_expire() {
        let dir = tempfile::tempdir().unwrap();
        let ap = Approvals::new(dir.path().to_path_buf());
        let a = ap.create("m1", "git.fetch", "fetch".into(), String::new(), Duration::ZERO).unwrap();
        assert!(!ap.wait(&a.id).await.unwrap());
        assert_eq!(ap.list().unwrap()[0].status, "expired");
        assert!(ap.decide(&a.id, true, "cli").is_err());
    }

    #[test]
    fn approvals_nobody_waits_for_expire() {
        let dir = tempfile::tempdir().unwrap();
        let ap = Approvals::new(dir.path().to_path_buf());
        let a = ap
            .create(
                "m1",
                "git.push",
                "push\x1b[2K\nx\u{202e}y".into(),
                "a\nb\x07".into(),
                Duration::from_secs(60),
            )
            .unwrap();
        assert_eq!(a.summary, "push [2K x y");
        assert_eq!(a.detail, "a\nb ");
        ap.expire_all();
        assert!(ap.decide(&a.id, true, "cli").is_err());
        for _ in 0..MAX_PENDING {
            ap.create("m2", "git.fetch", String::new(), String::new(), Duration::from_secs(60)).unwrap();
        }
        assert!(ap.create("m2", "git.fetch", String::new(), String::new(), Duration::from_secs(60)).is_err());
    }
}
