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
    pub decided_by: Option<String>,
    pub decided_at: Option<u64>,
}

/// Decided approvals kept for listing.
const KEEP_DECIDED: usize = 50;

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
        all.sort_by(|a, b| {
            (a.status != "pending").cmp(&(b.status != "pending")).then(b.created.cmp(&a.created))
        });
        Ok(all)
    }

    pub fn create(&self, machine: &str, kind: &str, summary: String, detail: String) -> io::Result<Approval> {
        let a = Approval {
            id: toby_config::new_id(),
            created: now(),
            machine: machine.into(),
            kind: kind.into(),
            summary,
            detail,
            status: "pending".into(),
            decided_by: None,
            decided_at: None,
        };
        self.store(&a)?;
        self.prune();
        Ok(a)
    }

    /// Records a decision; refused when the approval was already decided.
    pub fn decide(&self, id: &str, approve: bool, by: &str) -> io::Result<Approval> {
        let _lock = self.lock.lock().unwrap();
        let mut a = self.load(id)?;
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

    /// Waits for the decision; an approval not decided in time expires.
    /// Returns whether it was approved.
    pub async fn wait(&self, id: &str, timeout: Duration) -> io::Result<bool> {
        let notify = self.waiting.lock().unwrap().entry(id.into()).or_default().clone();
        let deadline = tokio::time::Instant::now() + timeout;
        let result = loop {
            let notified = notify.notified();
            let a = self.load(id)?;
            match a.status.as_str() {
                "approved" => break Ok(true),
                "pending" => {}
                _ => break Ok(false),
            }
            if tokio::time::timeout_at(deadline, notified).await.is_err() {
                let _lock = self.lock.lock().unwrap();
                let mut a = self.load(id)?;
                if a.status == "pending" {
                    a.status = "expired".into();
                    a.decided_at = Some(now());
                    self.store(&a)?;
                    break Ok(false);
                }
                break Ok(a.status == "approved");
            }
        };
        self.waiting.lock().unwrap().remove(id);
        result
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
        let a = ap.create("m1", "git.push", "push".into(), "detail".into()).unwrap();
        let waiter = {
            let ap = ap.clone();
            let id = a.id.clone();
            tokio::spawn(async move { ap.wait(&id, Duration::from_secs(10)).await })
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
        let a = ap.create("m1", "git.commit", "commit".into(), String::new()).unwrap();
        assert!(!ap.wait(&a.id, Duration::from_millis(50)).await.unwrap());
        assert_eq!(ap.list().unwrap()[0].status, "expired");
        assert!(ap.decide(&a.id, true, "cli").is_err());
    }
}
