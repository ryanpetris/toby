//! The progress display (plan §15.3): the steps of what a command does, as
//! `docker buildx` shows a build. In a terminal it is a block redrawn in
//! place: a header with the time and the steps done, a line for each step
//! with its own time, and the last lines of output under the step that
//! runs. Finished steps fold into their line; a failed one keeps its
//! output. Elsewhere the same events are plain lines (`#3 name`,
//! `#3 1.2 output`, `#3 DONE 4.5s`). `TOBY_PROGRESS` chooses: `plain`,
//! `quiet` (warnings and errors only) or the default.
//!
//! Nothing is shown until a step starts, so a command with nothing to do
//! prints nothing.

use std::collections::{HashMap, VecDeque};
use std::io::{IsTerminal, Write};
use std::time::{Duration, Instant};

use toby_api::progress::{self, Event, Plain};
use unicode_width::{UnicodeWidthChar, UnicodeWidthStr};

/// Output lines shown under a running step.
const TAIL: usize = 6;
/// Output lines shown under a failed step.
const FAILED_TAIL: usize = 20;
/// Output lines kept for each step.
const KEEP: usize = 40;
/// How often the block is redrawn.
const REDRAW: Duration = Duration::from_millis(100);

const DIM: &str = "\x1b[2m";
const BOLD: &str = "\x1b[1m";
const GREEN: &str = "\x1b[32m";
const CYAN: &str = "\x1b[36m";
const RED: &str = "\x1b[31m";
const RESET: &str = "\x1b[0m";

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Mode {
    Terminal,
    Plain,
    Quiet,
}

impl Mode {
    /// From `TOBY_PROGRESS`, and whether standard error is a terminal.
    pub fn detect() -> Mode {
        match std::env::var("TOBY_PROGRESS").as_deref() {
            Ok("plain") => Mode::Plain,
            Ok("quiet") => Mode::Quiet,
            _ if std::io::stderr().is_terminal() && std::env::var("TERM").as_deref() != Ok("dumb") => {
                Mode::Terminal
            }
            _ => Mode::Plain,
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum State {
    Queued,
    Running,
    Done,
    UpToDate,
    Failed,
}

#[derive(Debug)]
struct Count {
    done: u64,
    total: Option<u64>,
    bytes: bool,
    /// When the count was first seen, and its value then: for the rate.
    first: (u64, u64),
    at: u64,
}

#[derive(Debug)]
struct Step {
    name: String,
    parent: Option<u32>,
    state: State,
    /// Milliseconds since the display started.
    started: Option<u64>,
    finished: Option<u64>,
    count: Option<Count>,
    tail: VecDeque<String>,
}

/// The steps of one job of the daemon, placed under a step of the display.
pub struct Job {
    parent: Option<u32>,
    /// A parent step made when the job reports its first step.
    lazy: Option<String>,
    ids: HashMap<u32, u32>,
    /// Milliseconds after the display's start at which the job started.
    epoch: Option<u64>,
}

impl Job {
    /// The step the job's steps are part of, if it has one yet.
    pub fn parent(&self) -> Option<u32> {
        self.parent
    }
}

pub struct Display {
    mode: Mode,
    title: String,
    started: Instant,
    /// Step `n` is at index `n - 1`.
    steps: Vec<Step>,
    plain: Plain,
    /// Whether the block has been drawn.
    visible: bool,
    /// Lines of the block on the screen.
    drawn: usize,
    last_draw: Option<Instant>,
    finished: Option<bool>,
    /// Warnings printed, each once.
    warned: std::collections::HashSet<String>,
}

impl Display {
    pub fn new(title: impl Into<String>) -> Display {
        Display::with_mode(title, Mode::detect())
    }

    pub fn with_mode(title: impl Into<String>, mode: Mode) -> Display {
        Display {
            mode,
            title: title.into(),
            started: Instant::now(),
            steps: Vec::new(),
            plain: Plain::new(0),
            visible: false,
            drawn: 0,
            last_draw: None,
            finished: None,
            warned: Default::default(),
        }
    }

    fn now(&self) -> u64 {
        self.started.elapsed().as_millis() as u64
    }

    fn step_mut(&mut self, id: u32) -> Option<&mut Step> {
        self.steps.get_mut(id.checked_sub(1)? as usize)
    }

    fn step(&self, id: u32) -> Option<&Step> {
        self.steps.get(id.checked_sub(1)? as usize)
    }

    fn add(&mut self, name: String, parent: Option<u32>, state: State) -> u32 {
        self.steps.push(Step {
            name,
            parent,
            state,
            started: None,
            finished: None,
            count: None,
            tail: VecDeque::new(),
        });
        self.steps.len() as u32
    }

    /// A step to come, shown once something starts.
    pub fn queue(&mut self, name: impl Into<String>) -> u32 {
        self.add(name.into(), None, State::Queued)
    }

    /// Starts a queued step.
    pub fn start(&mut self, id: u32) {
        let Some(s) = self.step(id) else { return };
        let (name, parent) = (s.name.clone(), s.parent);
        let t = self.now();
        self.apply(Event::Start { id, parent, name, t });
    }

    /// A new step, started now.
    pub fn begin(&mut self, name: impl Into<String>, parent: Option<u32>) -> u32 {
        let id = self.add(name.into(), parent, State::Queued);
        self.start(id);
        id
    }

    pub fn end(&mut self, id: u32) {
        let t = self.now();
        self.apply(Event::Done { id, t });
    }

    /// A job whose steps go under `parent`.
    pub fn job(&self, parent: Option<u32>) -> Job {
        Job { parent, lazy: None, ids: HashMap::new(), epoch: None }
    }

    /// A job whose steps go under a step called `name`, made only if the
    /// job reports any.
    pub fn lazy_job(&self, name: impl Into<String>) -> Job {
        Job { parent: None, lazy: Some(name.into()), ids: HashMap::new(), epoch: None }
    }

    /// An event of `job`, with the job's step numbers and times.
    pub fn job_event(&mut self, job: &mut Job, e: Event) {
        let now = self.now();
        let epoch = *job.epoch.get_or_insert_with(|| match &e {
            Event::Start { t, .. } | Event::Done { t, .. } | Event::Failed { t, .. } => {
                now.saturating_sub(*t)
            }
            Event::Count { t, .. } | Event::Output { t, .. } => now.saturating_sub(*t),
            _ => now,
        });
        if job.parent.is_none()
            && !matches!(e, Event::Warning { .. })
            && let Some(name) = job.lazy.take()
        {
            job.parent = Some(self.begin(name, None));
        }
        let map = |job: &Job, id: u32| job.ids.get(&id).copied();
        let e = match e {
            Event::Start { id, parent, name, t } => {
                let parent = parent.and_then(|p| map(job, p)).or(job.parent);
                let global = self.add(name.clone(), parent, State::Queued);
                job.ids.insert(id, global);
                Event::Start { id: global, parent, name, t: epoch + t }
            }
            Event::UpToDate { id, parent, name } => {
                let parent = parent.and_then(|p| map(job, p)).or(job.parent);
                let global = self.add(name.clone(), parent, State::Queued);
                job.ids.insert(id, global);
                Event::UpToDate { id: global, parent, name }
            }
            Event::Done { id, t } => match map(job, id) {
                Some(id) => Event::Done { id, t: epoch + t },
                None => return,
            },
            Event::Failed { id, t } => match map(job, id) {
                Some(id) => Event::Failed { id, t: epoch + t },
                None => return,
            },
            Event::Count { id, done, total, bytes, t } => match map(job, id) {
                Some(id) => Event::Count { id, done, total, bytes, t: epoch + t },
                None => return,
            },
            Event::Output { id, line, t } => {
                Event::Output { id: id.and_then(|i| map(job, i)).or(job.parent), line, t: epoch + t }
            }
            w @ Event::Warning { .. } => w,
        };
        self.apply(e);
    }

    /// Records an event in the display's own numbering and shows it.
    fn apply(&mut self, e: Event) {
        match &e {
            Event::Start { id, t, .. } => {
                if let Some(s) = self.step_mut(*id) {
                    s.state = State::Running;
                    s.started = Some(*t);
                }
            }
            Event::UpToDate { id, .. } => {
                if let Some(s) = self.step_mut(*id) {
                    s.state = State::UpToDate;
                }
            }
            Event::Done { id, t } => {
                if let Some(s) = self.step_mut(*id) {
                    s.state = State::Done;
                    s.finished = Some(*t);
                }
            }
            Event::Failed { id, t } => {
                if let Some(s) = self.step_mut(*id) {
                    s.state = State::Failed;
                    s.finished = Some(*t);
                }
            }
            Event::Count { id, done, total, bytes, t } => {
                if let Some(s) = self.step_mut(*id) {
                    let first = s.count.as_ref().map_or((*t, *done), |c| c.first);
                    s.count = Some(Count { done: *done, total: *total, bytes: *bytes, first, at: *t });
                }
            }
            Event::Output { id: Some(id), line, .. } => {
                if let Some(s) = self.step_mut(*id) {
                    s.tail.push_back(line.clone());
                    if s.tail.len() > KEEP {
                        s.tail.pop_front();
                    }
                }
            }
            Event::Output { id: None, line, .. } => {
                let line = line.clone();
                self.above(&line);
                return;
            }
            Event::Warning { message } => {
                if self.warned.insert(message.clone()) {
                    let message = message.clone();
                    self.above(&message);
                }
                return;
            }
        }
        match self.mode {
            Mode::Plain => {
                let lines = self.plain.format(&e);
                let mut err = std::io::stderr().lock();
                for l in lines {
                    let _ = writeln!(err, "{l}");
                }
            }
            Mode::Terminal => {
                if matches!(e, Event::Start { .. } | Event::UpToDate { .. }) && !self.visible {
                    self.visible = true;
                    self.draw();
                } else if matches!(e, Event::Done { .. } | Event::Failed { .. }) {
                    self.draw();
                }
            }
            Mode::Quiet => {}
        }
    }

    /// A line printed above the block: a warning, or output of no step.
    fn above(&mut self, line: &str) {
        let mut err = std::io::stderr().lock();
        if self.mode == Mode::Terminal && self.drawn > 0 {
            let _ = write!(err, "\x1b[{}F\x1b[J", self.drawn);
            self.drawn = 0;
        }
        let _ = writeln!(err, "{line}");
        drop(err);
        if self.mode == Mode::Terminal && self.visible {
            self.draw();
        }
    }

    /// Warnings of an API response the user has not suppressed.
    pub fn warnings(&mut self, settings: &toby_config::global::Settings, warnings: &[toby_api::Warning]) {
        for w in warnings.iter().filter(|w| !settings.suppressed(&w.id)) {
            let message = format!("warning[{}]: {}", w.id, w.message);
            if self.warned.insert(message.clone()) {
                self.above(&message);
            }
        }
    }

    /// Redraws the block if it is due.
    pub fn tick(&mut self) {
        if self.mode == Mode::Terminal && self.visible && self.last_draw.is_none_or(|l| l.elapsed() >= REDRAW)
        {
            self.draw();
        }
    }

    /// Leaves the block where it is: what is printed next goes below it,
    /// and the block is drawn anew under that.
    pub fn leave(&mut self) {
        self.drawn = 0;
    }

    /// The command is done: the block is drawn a last time, folded, or
    /// with the failed step's output when it failed.
    pub fn finish(&mut self, ok: bool) {
        if self.finished.is_some() {
            return;
        }
        let t = self.now();
        if !ok {
            for s in self.steps.iter_mut().filter(|s| s.state == State::Running) {
                s.state = State::Failed;
                s.finished = Some(t);
            }
        }
        self.finished = Some(ok);
        if self.mode == Mode::Terminal && self.visible {
            self.draw();
        }
    }

    fn draw(&mut self) {
        let (rows, cols) =
            crossterm::terminal::size().map(|(c, r)| (usize::from(r), usize::from(c))).unwrap_or((24, 80));
        let lines = self.frame(self.now(), cols, rows);
        let mut out = String::new();
        if self.drawn > 0 {
            out.push_str(&format!("\x1b[{}F", self.drawn));
        }
        for l in &lines {
            out.push_str("\x1b[2K");
            out.push_str(l);
            out.push('\n');
        }
        out.push_str("\x1b[J");
        let mut err = std::io::stderr().lock();
        let _ = err.write_all(out.as_bytes());
        let _ = err.flush();
        self.drawn = lines.len();
        self.last_draw = Some(Instant::now());
    }

    fn children(&self, id: Option<u32>) -> impl Iterator<Item = u32> + '_ {
        self.steps.iter().enumerate().filter(move |(_, s)| s.parent == id).map(|(i, _)| i as u32 + 1)
    }

    /// The block's lines at `now`, fitted to the terminal.
    fn frame(&self, now: u64, cols: usize, rows: usize) -> Vec<String> {
        let width = cols.saturating_sub(1).max(20);
        let counted: Vec<&Step> = self.steps.iter().filter(|s| s.state != State::Queued).collect();
        let done = self.steps.iter().filter(|s| matches!(s.state, State::Done | State::UpToDate)).count();
        let status = match self.finished {
            Some(true) => " FINISHED",
            Some(false) => " FAILED",
            None => "",
        };
        let header = format!(
            "[+] {} {}  ({done}/{}){status}",
            self.title,
            progress::elapsed(now),
            self.steps.len().max(counted.len())
        );
        let mut blocks: Vec<(bool, Vec<String>)> = Vec::new();
        for top in self.children(None) {
            let mut lines = Vec::new();
            self.rows(top, 0, now, width, &mut lines);
            let finished = self.step(top).is_some_and(|s| matches!(s.state, State::Done | State::UpToDate));
            blocks.push((finished, lines));
        }
        // Too tall: finished steps at the top go first, then the oldest lines.
        let room = rows.saturating_sub(2).max(3);
        let mut hidden = 0;
        while blocks.iter().map(|b| b.1.len()).sum::<usize>() + usize::from(hidden > 0) > room
            && blocks.len() > 1
            && blocks.first().is_some_and(|b| b.0)
        {
            blocks.remove(0);
            hidden += 1;
        }
        let mut lines: Vec<String> = Vec::new();
        if hidden > 0 {
            lines.push(format!(" {DIM}✔ {hidden} more{RESET}"));
        }
        lines.extend(blocks.into_iter().flat_map(|b| b.1));
        if lines.len() > room {
            lines.drain(..lines.len() - room);
        }
        lines.insert(0, format!("{BOLD}{}{RESET}", fit(&header, width)));
        lines
    }

    fn rows(&self, id: u32, depth: usize, now: u64, width: usize, out: &mut Vec<String>) {
        let Some(s) = self.step(id) else { return };
        let indent = " ".repeat(1 + 2 * depth);
        let (glyph, color) = match s.state {
            State::Queued => ("○", DIM),
            State::Running => ("●", CYAN),
            State::Done => ("✔", GREEN),
            State::UpToDate => ("✔", DIM),
            State::Failed => ("✘", RED),
        };
        let time = match s.state {
            State::Queued => String::new(),
            State::UpToDate => "up to date".into(),
            State::Running => s.started.map(|t| progress::elapsed(now.saturating_sub(t))).unwrap_or_default(),
            State::Done | State::Failed => match (s.started, s.finished) {
                (Some(a), Some(b)) => progress::elapsed(b.saturating_sub(a)),
                _ => String::new(),
            },
        };
        let mut name = s.name.clone();
        if let Some(c) = &s.count
            && s.state == State::Running
        {
            name.push_str("  ");
            name.push_str(&progress::count(c.done, c.total, c.bytes));
            let secs = c.at.saturating_sub(c.first.0) as f64 / 1000.0;
            if c.bytes && secs >= 1.0 {
                let rate = (c.done.saturating_sub(c.first.1) as f64 / secs) as u64;
                name.push_str(&format!("  {}/s", progress::size(rate)));
            }
        }
        let left_room = width.saturating_sub(time.width() + indent.len() + 4);
        let name = fit(&name, left_room);
        let pad = width.saturating_sub(indent.len() + 2 + name.width() + time.width()).max(1);
        let dim = if matches!(s.state, State::Queued | State::UpToDate) { DIM } else { "" };
        out.push(format!(
            "{indent}{color}{glyph}{RESET} {dim}{name}{RESET}{}{DIM}{time}{RESET}",
            " ".repeat(pad)
        ));
        let open = matches!(s.state, State::Running | State::Failed);
        let mut children = self.children(Some(id)).peekable();
        let leaf = children.peek().is_none();
        if open {
            for c in children {
                self.rows(c, depth + 1, now, width, out);
            }
        }
        // Output under the step that runs, or the failed step that ended the
        // command.
        let running_child =
            self.children(Some(id)).any(|c| self.step(c).is_some_and(|c| c.state == State::Running));
        let failed_child =
            self.children(Some(id)).any(|c| self.step(c).is_some_and(|c| c.state == State::Failed));
        let tail = match s.state {
            State::Running if !running_child => TAIL,
            State::Failed if self.finished.is_some() && (leaf || !failed_child) => FAILED_TAIL,
            _ => 0,
        };
        let pad = " ".repeat(3 + 2 * depth);
        for line in s.tail.iter().skip(s.tail.len().saturating_sub(tail)) {
            out.push(format!("{pad}{DIM}{}{RESET}", fit(line, width.saturating_sub(pad.len()))));
        }
    }
}

/// `text` cut to `width` columns, with `…` where it was cut.
fn fit(text: &str, width: usize) -> String {
    if text.width() <= width {
        return text.to_string();
    }
    let mut out = String::new();
    let mut used = 0;
    for c in text.chars() {
        let w = c.width().unwrap_or(0);
        if used + w + 1 > width {
            break;
        }
        out.push(c);
        used += w;
    }
    out.push('…');
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    /// The frame without colours.
    fn plain(d: &Display, now: u64, cols: usize, rows: usize) -> Vec<String> {
        let strip = |s: &str| {
            let mut out = String::new();
            let mut esc = false;
            for c in s.chars() {
                match (esc, c) {
                    (false, '\x1b') => esc = true,
                    (true, 'm') => esc = false,
                    (true, _) => {}
                    (false, c) => out.push(c),
                }
            }
            out.trim_end().to_string()
        };
        d.frame(now, cols, rows).iter().map(|l| strip(l)).collect()
    }

    #[test]
    fn a_launch_as_a_block() {
        let mut d = Display::with_mode("Setting up claude", Mode::Quiet);
        let home = d.queue("Home default");
        let machine = d.queue("Machine default/default");
        d.start(home);
        let mut job = d.job(Some(home));
        d.job_event(
            &mut job,
            Event::Start { id: 1, parent: None, name: "Downloading debian.qcow2".into(), t: 0 },
        );
        d.job_event(
            &mut job,
            Event::Count { id: 1, done: 100 << 20, total: Some(612 << 20), bytes: true, t: 2000 },
        );
        d.job_event(&mut job, Event::Output { id: Some(1), line: "resolving".into(), t: 10 });
        let frame = plain(&d, 3000, 80, 24);
        assert_eq!(frame[0], "[+] Setting up claude 3.0s  (0/3)");
        assert!(frame[1].starts_with(" ● Home default"), "{frame:?}");
        assert!(frame[2].starts_with("   ● Downloading debian.qcow2  100.0 MiB / 612.0 MiB"), "{frame:?}");
        assert_eq!(frame[3].trim(), "resolving");
        assert!(frame[4].starts_with(" ○ Machine default/default"));

        d.job_event(&mut job, Event::Done { id: 1, t: 2500 });
        d.end(home);
        d.start(machine);
        let mut boot = d.job(Some(machine));
        d.job_event(&mut boot, Event::Start { id: 1, parent: None, name: "Booting".into(), t: 0 });
        d.job_event(&mut boot, Event::Failed { id: 1, t: 1500 });
        d.finish(false);
        let frame = plain(&d, 6000, 60, 24);
        assert!(frame[0].ends_with("FAILED"), "{frame:?}");
        assert!(frame[1].starts_with(" ✔ Home default"), "{frame:?}");
        assert_eq!(frame.len(), 4, "the finished step is folded: {frame:?}");
        assert!(frame[2].starts_with(" ✘ Machine default/default"));
        assert!(frame[3].starts_with("   ✘ Booting") && frame[3].ends_with("1.5s"), "{frame:?}");
    }

    #[test]
    fn nothing_shows_until_a_step_starts() {
        let mut d = Display::with_mode("x", Mode::Terminal);
        d.queue("Home default");
        let mut job = d.lazy_job("claude");
        d.job_event(&mut job, Event::Warning { message: "w".into() });
        assert!(!d.visible && job.parent().is_none());
    }

    #[test]
    fn tall_blocks_drop_finished_steps_first() {
        let mut d = Display::with_mode("x", Mode::Quiet);
        for i in 0..10 {
            let id = d.begin(format!("step {i}"), None);
            d.end(id);
        }
        let last = d.begin("running", None);
        let _ = last;
        let frame = plain(&d, 1000, 40, 8);
        assert_eq!(frame.len(), 7);
        assert!(frame[1].contains("more"), "{frame:?}");
        assert!(frame.last().unwrap().contains("running"));
        assert_eq!(fit("abcdef", 4), "abc…");
    }
}
