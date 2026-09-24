//! Builder jobs run by tobyd (image builds, bootstrap, home formatting):
//! their output is kept in memory and in a log file, and streamed to
//! clients (plan §15.3).

use std::collections::HashMap;
use std::future::Future;
use std::io::Write;
use std::path::PathBuf;
use std::pin::Pin;
use std::sync::{Arc, Mutex};

use toby_api::BuildStatus;
use tokio::sync::watch;

use crate::builder::Output;

/// A running job: it returns the image it produced, if any.
pub type JobFuture<'a> = Pin<Box<dyn Future<Output = std::io::Result<Option<String>>> + Send + 'a>>;

pub struct Build {
    pub id: String,
    pub log_path: PathBuf,
    inner: Mutex<Inner>,
    /// Bumped whenever output is added or the build finishes.
    changed: watch::Sender<u64>,
}

struct Inner {
    output: Vec<u8>,
    state: &'static str,
    error: Option<String>,
    image: Option<String>,
}

impl Build {
    pub fn status(&self) -> BuildStatus {
        let inner = self.inner.lock().unwrap();
        BuildStatus {
            id: self.id.clone(),
            state: inner.state.into(),
            error: inner.error.clone(),
            image: inner.image.clone(),
            log: self.log_path.display().to_string(),
        }
    }

    /// Output from `offset` on, and whether the build has finished.
    pub fn output_from(&self, offset: usize) -> (Vec<u8>, bool) {
        let inner = self.inner.lock().unwrap();
        (inner.output.get(offset..).unwrap_or_default().to_vec(), inner.state != "running")
    }

    pub fn subscribe(&self) -> watch::Receiver<u64> {
        self.changed.subscribe()
    }

    fn append(&self, bytes: &[u8]) {
        self.inner.lock().unwrap().output.extend_from_slice(bytes);
        self.changed.send_modify(|n| *n += 1);
    }

    fn finish(&self, result: Result<Option<String>, String>) {
        {
            let mut inner = self.inner.lock().unwrap();
            match result {
                Ok(image) => {
                    inner.state = "succeeded";
                    inner.image = image;
                }
                Err(e) => {
                    inner.state = "failed";
                    inner.error = Some(e);
                }
            }
        }
        self.changed.send_modify(|n| *n += 1);
    }
}

/// How long a finished build's output stays in memory; its log stays.
const KEEP_FINISHED: std::time::Duration = std::time::Duration::from_secs(3600);

#[derive(Default)]
pub struct Builds {
    builds: Arc<Mutex<HashMap<String, Arc<Build>>>>,
}

impl Builds {
    pub fn get(&self, id: &str) -> Option<Arc<Build>> {
        self.builds.lock().unwrap().get(id).cloned()
    }

    /// Starts `job` in the background; its output goes to the build's log.
    /// The job returns the image it produced, if any.
    pub fn start<F>(&self, logs: PathBuf, kind: &str, job: F) -> std::io::Result<Arc<Build>>
    where
        F: for<'a> FnOnce(Output<'a>) -> JobFuture<'a> + Send + 'static,
    {
        std::fs::create_dir_all(&logs)?;
        let id = toby_config::new_id();
        let log_path = logs.join(format!("{id}-{kind}.log"));
        let mut file = std::fs::File::create(&log_path)?;
        let build = Arc::new(Build {
            id: id.clone(),
            log_path,
            inner: Mutex::new(Inner { output: Vec::new(), state: "running", error: None, image: None }),
            changed: watch::channel(0).0,
        });
        self.builds.lock().unwrap().insert(id.clone(), build.clone());
        let b = build.clone();
        let builds = self.builds.clone();
        tokio::spawn(async move {
            let mut out = |bytes: &[u8], _stderr: bool| {
                let _ = file.write_all(bytes);
                b.append(bytes);
            };
            let result = job(&mut out).await.map_err(|e| e.to_string());
            if let Err(e) = &result {
                out(format!("toby: {e}\n").as_bytes(), true);
            }
            b.finish(result);
            tokio::time::sleep(KEEP_FINISHED).await;
            builds.lock().unwrap().remove(&id);
        });
        Ok(build)
    }
}
