//! The terminal compositor (plan §13.5). A session's output goes to the
//! terminal as it is, so the terminal's own scrollback, colors, mouse and
//! paste keep working; only scroll regions and absolute rows are kept
//! within the session's rows. The last row holds a status line below the
//! scroll region, and approvals open as overlays over the session.
//!
//! Two emulators follow along: the session's screen (all rows but the last),
//! which says what belongs under an overlay, and a mirror of the terminal
//! fed with everything written to it, which says where the terminal's cursor
//! is, with which attributes, character sets and modes, so they can be put
//! back after drawing. Drawing waits until the output is between escape
//! sequences, strings and UTF-8 characters.

use std::collections::HashMap;
use std::time::{Duration, Instant};

use alacritty_terminal::event::VoidListener;
use alacritty_terminal::grid::Dimensions;
use alacritty_terminal::index::{Column, Line};
use alacritty_terminal::term::cell::{Cell, Flags};
use alacritty_terminal::term::{Config, TermMode};
use alacritty_terminal::vte::ansi::{CharsetIndex, Color, Processor, StandardCharset, Timeout};
use unicode_width::UnicodeWidthChar;

/// What the status line and the approval overlay show.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Status {
    /// Items of the status line, such as the machine.
    pub items: Vec<String>,
    /// Approvals waiting for the user.
    pub approvals: Vec<Approval>,
    /// Approvals whose decision here did not reach tobyd.
    pub failed: Vec<String>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Approval {
    pub id: String,
    /// The action, such as `git.push`.
    pub kind: String,
    pub summary: String,
    pub detail: String,
}

/// A decision made in the overlay: the approval and whether it was
/// approved.
pub type Decision = (String, bool);

/// Keys typed this soon after an overlay opened by itself, or moved on to
/// the next approval, are ignored: they were meant for the session.
const ARMING: Duration = Duration::from_millis(500);
/// How long an answered approval stays hidden if tobyd does not confirm
/// the decision.
const ANSWERED_FOR: Duration = Duration::from_secs(10);
/// Longest control sequence kept; longer ones are dropped, as terminals
/// ignore them.
const MAX_SEQUENCE: usize = 256;
/// Lines the emulators keep above their screens.
const HISTORY: usize = 1000;
/// What a soft reset (`CSI ! p`) resets, for the mirror, whose emulator
/// does not know it.
const SOFT_RESET: &[u8] = b"\x1b[4l\x1b[?6l\x1b(B\x1b)B\x1b*B\x1b+B\x0f\x1b[0m";

const BAR_STYLE: &str = "\x1b[0;38;2;232;232;227;48;2;52;52;50m";
const OVERLAY_STYLE: &str = "\x1b[0;38;2;232;232;227;48;2;32;32;30m";
const OVERLAY_TITLE: &str = "\x1b[0;1;38;2;150;180;255;48;2;32;32;30m";

/// An emulator's size.
struct Size {
    rows: usize,
    cols: usize,
}

impl Dimensions for Size {
    fn total_lines(&self) -> usize {
        self.rows
    }

    fn screen_lines(&self) -> usize {
        self.rows
    }

    fn columns(&self) -> usize {
        self.cols
    }
}

/// Synchronized updates are applied as they arrive, as a terminal does
/// (it only holds back showing them).
#[derive(Default)]
struct Immediate;

impl Timeout for Immediate {
    fn set_timeout(&mut self, _: Duration) {}
    fn clear_timeout(&mut self) {}
    fn pending_timeout(&self) -> bool {
        false
    }
}

struct Screen {
    term: alacritty_terminal::Term<VoidListener>,
    parser: Processor<Immediate>,
}

impl Screen {
    fn new(rows: u16, cols: u16) -> Screen {
        // History lets a resize bring lines back, as terminals do.
        let config = Config { scrolling_history: HISTORY, ..Config::default() };
        let size = Size { rows: rows.into(), cols: cols.into() };
        Screen { term: alacritty_terminal::Term::new(config, &size, VoidListener), parser: Processor::new() }
    }

    fn feed(&mut self, bytes: &[u8]) {
        self.parser.advance(&mut self.term, bytes);
    }

    fn resize(&mut self, rows: u16, cols: u16) {
        self.term.resize(Size { rows: rows.into(), cols: cols.into() });
    }

    fn cell(&self, row: usize, col: usize) -> &Cell {
        &self.term.grid()[Line(row as i32)][Column(col)]
    }

    fn cols(&self) -> usize {
        self.term.columns()
    }

    /// The text of a row.
    fn text(&self, row: usize) -> String {
        (0..self.cols())
            .map(|c| self.cell(row, c))
            .filter(|c| !c.flags.contains(Flags::WIDE_CHAR_SPACER))
            .map(|c| c.c)
            .collect()
    }
}

/// SGR parameters for a color, as foreground (`base` 30) or background (40).
fn color(c: Color, base: u8, out: &mut String) {
    match c {
        Color::Named(n) if (n as usize) < 8 => out.push_str(&format!(";{}", base + n as u8)),
        Color::Named(n) if (n as usize) < 16 => out.push_str(&format!(";{}", base + 60 + n as u8 - 8)),
        Color::Named(_) => {}
        Color::Indexed(i) => out.push_str(&format!(";{};5;{i}", base + 8)),
        Color::Spec(rgb) => out.push_str(&format!(";{};2;{};{};{}", base + 8, rgb.r, rgb.g, rgb.b)),
    }
}

/// The SGR sequence that sets exactly `cell`'s attributes.
fn sgr(cell: &Cell) -> String {
    let mut p = String::from("\x1b[0");
    let f = cell.flags;
    for (flag, code) in [
        (Flags::BOLD, ";1"),
        (Flags::DIM, ";2"),
        (Flags::ITALIC, ";3"),
        (Flags::UNDERLINE, ";4"),
        (Flags::DOUBLE_UNDERLINE, ";4:2"),
        (Flags::UNDERCURL, ";4:3"),
        (Flags::DOTTED_UNDERLINE, ";4:4"),
        (Flags::DASHED_UNDERLINE, ";4:5"),
        (Flags::INVERSE, ";7"),
        (Flags::HIDDEN, ";8"),
        (Flags::STRIKEOUT, ";9"),
    ] {
        if f.contains(flag) {
            p.push_str(code);
        }
    }
    color(cell.fg, 30, &mut p);
    color(cell.bg, 40, &mut p);
    if let Some(u) = cell.underline_color() {
        match u {
            Color::Indexed(i) => p.push_str(&format!(";58;5;{i}")),
            Color::Spec(rgb) => p.push_str(&format!(";58;2;{};{};{}", rgb.r, rgb.g, rgb.b)),
            Color::Named(n) if (n as usize) < 16 => p.push_str(&format!(";58;5;{}", n as u8)),
            Color::Named(_) => {}
        }
    }
    p.push('m');
    p
}

/// The OSC 8 sequence for `cell`'s hyperlink, or the one ending a link.
fn link(cell: &Cell) -> String {
    match cell.hyperlink() {
        Some(h) => format!("\x1b]8;id={};{}\x1b\\", h.id(), h.uri()),
        None => "\x1b]8;;\x1b\\".into(),
    }
}

fn same_look(a: &Cell, b: &Cell) -> bool {
    let ul = |c: &Cell| c.underline_color();
    a.fg == b.fg
        && a.bg == b.bg
        && a.flags.difference(Flags::WRAPLINE) == b.flags.difference(Flags::WRAPLINE)
        && ul(a) == ul(b)
}

fn same_link(a: &Cell, b: &Cell) -> bool {
    let l = |c: &Cell| c.hyperlink();
    l(a) == l(b)
}

fn same_cell(a: &Cell, b: &Cell) -> bool {
    let z = |c: &Cell| c.zerowidth().map(<[char]>::to_vec).unwrap_or_default();
    a.c == b.c && same_look(a, b) && same_link(a, b) && z(a) == z(b)
}

/// Writes columns `from..to` of row `row` of `screen` at the terminal's
/// row `row + 1`.
fn write_row(screen: &Screen, row: usize, from: usize, to: usize, out: &mut Vec<u8>) {
    let mut s = format!("\x1b[{};{}H\x1b[0m\x1b]8;;\x1b\\", row + 1, from + 1);
    let blank = Cell::default();
    let mut prev = &blank;
    let cols = screen.cols();
    for col in from..to {
        let cell = screen.cell(row, col);
        if cell.flags.contains(Flags::WIDE_CHAR_SPACER) {
            continue;
        }
        if col == from || !same_look(cell, prev) {
            s.push_str(&sgr(cell));
        }
        if !same_link(cell, prev) {
            s.push_str(&link(cell));
        }
        let wide = cell.flags.contains(Flags::WIDE_CHAR);
        if cell.flags.contains(Flags::LEADING_WIDE_CHAR_SPACER)
            || (wide && (col + 1 == cols || col + 1 == to))
        {
            s.push(' ');
        } else {
            s.push(cell.c);
            for z in cell.zerowidth().unwrap_or_default() {
                s.push(*z);
            }
        }
        prev = cell;
    }
    s.push_str("\x1b[0m\x1b]8;;\x1b\\");
    out.extend(s.as_bytes());
}

#[derive(Debug, Default, Clone, Copy, PartialEq, Eq)]
enum State {
    #[default]
    Ground,
    Escape,
    /// After ESC and an intermediate byte, such as `(`.
    EscapeIntermediate,
    Csi,
    /// A control sequence too long to keep, dropped up to its end.
    CsiIgnore,
    /// A string (OSC, DCS, APC, PM, SOS) until ST, or BEL for OSC.
    Text {
        osc: bool,
    },
    TextEscape {
        osc: bool,
    },
}

/// Follows the session's output on its way to the terminal: keeps absolute
/// rows and scroll regions within the session's rows, and knows whether
/// the output is between sequences and characters, where drawing may go.
#[derive(Debug, Default)]
struct Scan {
    state: State,
    /// A control sequence being collected, from its ESC.
    sequence: Vec<u8>,
    /// Continuation bytes the current UTF-8 character still needs.
    utf8_left: u8,
    /// The session's scroll region (1-based, inclusive).
    top: u16,
    bottom: u16,
    /// Which of G0–G3 is invoked into GL.
    gl: u8,
    /// The terminal may have lost the scroll region.
    region_lost: bool,
    /// Where in the output soft resets ended.
    soft_resets: Vec<usize>,
}

/// Parameters as numbers (empty is `None`, larger than `u16` the maximum);
/// `None` overall when one is not a plain number.
fn numbers(params: &[u8]) -> Option<Vec<Option<u16>>> {
    params
        .split(|b| *b == b';')
        .map(|n| match n {
            [] => Some(None),
            _ if n.iter().all(u8::is_ascii_digit) => Some(Some(
                std::str::from_utf8(n)
                    .ok()?
                    .parse::<u64>()
                    .map(|v| v.min(u16::MAX.into()) as u16)
                    .unwrap_or(u16::MAX),
            )),
            _ => None,
        })
        .collect()
}

impl Scan {
    fn new(limit: u16) -> Scan {
        Scan { top: 1, bottom: limit, ..Default::default() }
    }

    /// Between sequences and characters.
    fn at_rest(&self) -> bool {
        self.state == State::Ground && self.utf8_left == 0
    }

    /// Passes `input` to `out`, rewriting what would reach the status line.
    /// `limit` is the session's last row.
    fn feed(&mut self, input: &[u8], limit: u16, out: &mut Vec<u8>) {
        for &b in input {
            self.byte(b, limit, out);
        }
    }

    fn byte(&mut self, b: u8, limit: u16, out: &mut Vec<u8>) {
        // Cancel and substitute end any sequence.
        if matches!(b, 0x18 | 0x1a) && self.state != State::Ground {
            self.sequence.clear();
            self.state = State::Ground;
            out.push(b);
            return;
        }
        match self.state {
            State::Ground => match b {
                0x1b => {
                    self.state = State::Escape;
                    self.utf8_left = 0;
                    self.sequence.clear();
                    self.sequence.push(b);
                    return;
                }
                0x0e => self.gl = 1,
                0x0f => self.gl = 0,
                0x80..=0xbf => self.utf8_left = self.utf8_left.saturating_sub(1),
                0xc2..=0xdf => self.utf8_left = 1,
                0xe0..=0xef => self.utf8_left = 2,
                0xf0..=0xf4 => self.utf8_left = 3,
                _ => self.utf8_left = 0,
            },
            // The escape is written with the byte after it, or held with a
            // control sequence until it is complete.
            State::Escape => {
                match b {
                    // An escape starts over.
                    0x1b => return,
                    b'[' => {
                        self.sequence.push(b);
                        self.state = State::Csi;
                        return;
                    }
                    b']' => self.state = State::Text { osc: true },
                    b'P' | b'_' | b'^' | b'X' => self.state = State::Text { osc: false },
                    0x20..=0x2f => self.state = State::EscapeIntermediate,
                    b'c' => *self = Scan { region_lost: true, ..Scan::new(limit) },
                    b'n' => {
                        self.gl = 2;
                        self.state = State::Ground;
                    }
                    b'o' => {
                        self.gl = 3;
                        self.state = State::Ground;
                    }
                    // Controls run where they are.
                    0x00..=0x1f => {}
                    _ => self.state = State::Ground,
                }
                out.push(0x1b);
            }
            State::EscapeIntermediate => match b {
                0x1b => {
                    self.state = State::Escape;
                    self.sequence.clear();
                    self.sequence.push(b);
                    return;
                }
                0x30.. => self.state = State::Ground,
                _ => {}
            },
            State::CsiIgnore => {
                if (0x40..=0x7e).contains(&b) {
                    self.state = State::Ground;
                }
                return;
            }
            State::Csi => match b {
                // Another escape ends this one unfinished.
                0x1b => {
                    self.sequence.clear();
                    self.sequence.push(b);
                    self.state = State::Escape;
                    return;
                }
                // Controls run where they are.
                0x00..=0x1f => {}
                // Terminals ignore DEL inside a sequence.
                0x7f => return,
                0x40..=0x7e => {
                    self.sequence.push(b);
                    self.state = State::Ground;
                    let sequence = std::mem::take(&mut self.sequence);
                    self.csi(&sequence, limit, out);
                    self.sequence = sequence;
                    return;
                }
                _ => {
                    self.sequence.push(b);
                    if self.sequence.len() > MAX_SEQUENCE {
                        self.sequence.clear();
                        self.state = State::CsiIgnore;
                    }
                    return;
                }
            },
            State::Text { osc } => match b {
                0x07 if osc => self.state = State::Ground,
                0x1b => self.state = State::TextEscape { osc },
                _ => {}
            },
            State::TextEscape { .. } => {
                if b != b'\\' {
                    // Another escape ends the string and starts anew.
                    self.state = State::Escape;
                    self.sequence.clear();
                    self.sequence.push(0x1b);
                    // The escape itself was written with the string.
                    out.pop();
                    return self.byte(b, limit, out);
                }
                self.state = State::Ground;
            }
        }
        out.push(b);
    }

    /// A complete control sequence, `ESC [ … final`.
    fn csi(&mut self, sequence: &[u8], limit: u16, out: &mut Vec<u8>) {
        let body = &sequence[2..sequence.len() - 1];
        let fin = sequence[sequence.len() - 1];
        let (private, params) = match body.first() {
            Some(&c @ (b'?' | b'>' | b'<' | b'=')) => (Some(c), &body[1..]),
            _ => (None, body),
        };
        let intermediate = params.iter().any(|b| (0x20..=0x2f).contains(b));
        if private.is_none() && fin == b'p' && params == b"!" {
            // A soft reset: the margins, modes and character sets go.
            self.top = 1;
            self.bottom = limit;
            self.gl = 0;
            self.region_lost = true;
            out.extend_from_slice(sequence);
            self.soft_resets.push(out.len());
            return;
        }
        let moves = private.is_none() && !intermediate && matches!(fin, b'r' | b'H' | b'f' | b'd');
        let Some(n) = numbers(params) else {
            // A cursor move or region the terminal may read otherwise is
            // not passed on.
            if !moves {
                out.extend_from_slice(sequence);
            }
            return;
        };
        let first = n.first().copied().flatten().unwrap_or(0);
        match (private, intermediate, fin) {
            (None, false, b'r') => {
                let top = first.max(1);
                let bottom = n.get(1).copied().flatten().filter(|b| *b > 0).unwrap_or(limit).min(limit);
                // An invalid region is ignored, as terminals do.
                if top < bottom {
                    self.top = top;
                    self.bottom = bottom;
                    out.extend(format!("\x1b[{top};{bottom}r").as_bytes());
                }
                return;
            }
            (None, false, b'H' | b'f' | b'd') if first > limit => {
                let rest = params.iter().position(|b| *b == b';').map(|i| &params[i..]).unwrap_or(&[]);
                out.extend(format!("\x1b[{limit}").as_bytes());
                out.extend_from_slice(rest);
                out.push(fin);
                return;
            }
            (Some(b'?'), false, b'h' | b'l') if n.iter().flatten().any(|m| matches!(m, 47 | 1047 | 1049)) => {
                self.region_lost = true;
            }
            _ => {}
        }
        out.extend_from_slice(sequence);
    }
}

/// Display width of a string.
fn width(s: &str) -> usize {
    s.chars().map(|c| c.width().unwrap_or(0)).sum()
}

/// `s` cut or padded to exactly `cols` columns.
fn fit(s: &str, cols: usize) -> String {
    let mut out = String::new();
    let mut used = 0;
    for c in s.chars() {
        let w = c.width().unwrap_or(0);
        if used + w > cols {
            break;
        }
        out.push(c);
        used += w;
    }
    out.extend(std::iter::repeat_n(' ', cols - used));
    out
}

/// Lines of at most `cols` columns, broken at spaces where possible.
fn wrap(text: &str, cols: usize) -> Vec<String> {
    let mut lines = Vec::new();
    for paragraph in text.lines() {
        let mut line = String::new();
        for word in paragraph.split(' ') {
            let sep = usize::from(!line.is_empty());
            if width(&line) + sep + width(word) <= cols {
                if sep == 1 {
                    line.push(' ');
                }
                line.push_str(word);
                continue;
            }
            if !line.is_empty() {
                lines.push(std::mem::take(&mut line));
            }
            for c in word.chars() {
                if width(&line) + c.width().unwrap_or(0) > cols {
                    lines.push(std::mem::take(&mut line));
                }
                line.push(c);
            }
        }
        lines.push(line);
    }
    lines
}

/// At most `n` lines; the last shows an ellipsis when some were cut.
fn at_most(mut lines: Vec<String>, n: usize, cols: usize) -> Vec<String> {
    if lines.len() > n {
        lines.truncate(n);
        if let Some(last) = lines.last_mut() {
            let kept = fit(last, cols.saturating_sub(1)).trim_end().to_string();
            *last = format!("{kept}…");
        }
    }
    lines
}

/// What the terminal had before drawing, to be put back.
struct Saved {
    cursor: alacritty_terminal::grid::Cursor<Cell>,
    mode: TermMode,
    /// The cell under the cursor, when its line waits to wrap.
    wrap_cell: Option<Cell>,
}

/// The session's screen with a status line and overlays on the terminal.
pub struct Compositor {
    rows: u16,
    cols: u16,
    screen: Screen,
    mirror: Screen,
    scan: Scan,
    status: Status,
    /// The approval the overlay shows.
    shown: Option<String>,
    /// The drawn overlay: first row and column (1-based) and its lines.
    overlay: Option<(u16, u16, Vec<String>)>,
    /// When the overlay starts taking keys.
    armed: Instant,
    /// Approvals seen here, and answered here (when).
    seen: std::collections::HashSet<String>,
    answered: HashMap<String, Instant>,
    /// Something waits to be drawn until the output is at rest.
    pending: bool,
    /// The terminal was resized: where it moved its contents is its own
    /// choice, so all is drawn again from the session's screen.
    repaint: bool,
    /// A paste is coming in while the overlay is open; it is the session's.
    pasting: bool,
}

impl Compositor {
    /// Whether a terminal of this size has room for the status line and
    /// the overlay.
    pub fn fits(rows: u16, cols: u16) -> bool {
        rows >= 8 && cols >= 30
    }

    /// For a terminal of `rows`×`cols` (it has to `fit`).
    pub fn new(rows: u16, cols: u16) -> Compositor {
        Compositor {
            rows,
            cols,
            screen: Screen::new(rows - 1, cols),
            mirror: Screen::new(rows, cols),
            scan: Scan::new(rows - 1),
            status: Status::default(),
            shown: None,
            overlay: None,
            armed: Instant::now(),
            seen: Default::default(),
            answered: Default::default(),
            pending: false,
            repaint: false,
            pasting: false,
        }
    }

    /// The session's terminal size.
    pub fn session_size(&self) -> (u16, u16) {
        (self.rows - 1, self.cols)
    }

    fn limit(&self) -> u16 {
        self.rows - 1
    }

    /// Records bytes written to the terminal.
    fn emit(&mut self, out: &mut Vec<u8>, bytes: &[u8]) {
        self.mirror.feed(bytes);
        out.extend_from_slice(bytes);
    }

    /// Takes over the terminal: frees its last row for the status line.
    /// `cursor` is where the terminal's cursor is (1-based), if known;
    /// otherwise the screen is cleared.
    pub fn start(&mut self, cursor: Option<(u16, u16)>) -> Vec<u8> {
        let limit = self.limit();
        let mut out = Vec::new();
        let (row, col) = match cursor {
            // A new line keeps a row free below the cursor, which then goes
            // back up (apt's progress line does the same).
            Some((row, col)) => {
                self.mirror.feed(format!("\x1b[{};{col}H", row.min(self.rows)).as_bytes());
                self.emit(&mut out, format!("\n\x1b7\x1b[1;{limit}r\x1b8\x1b[A").as_bytes());
                (if row < self.rows { row } else { limit }, col)
            }
            None => {
                self.emit(&mut out, format!("\x1b[2J\x1b[1;{limit}r\x1b[H").as_bytes());
                (1, 1)
            }
        };
        self.screen.feed(format!("\x1b[{row};{col}H").as_bytes());
        self.draw(&mut out);
        out
    }

    /// Bytes for the terminal after the session wrote `bytes`.
    pub fn output(&mut self, bytes: &[u8]) -> Vec<u8> {
        let mut out = Vec::new();
        // Nothing drawn scrolls into the terminal's history with the output.
        if self.overlay.is_some() && self.scan.at_rest() {
            let saved = self.save();
            self.neutral(&saved, &mut out);
            self.overlay = None;
            self.sync_rows(&mut out);
            self.restore(&saved, &mut out);
        }
        let mut pass = Vec::with_capacity(bytes.len());
        self.scan.feed(bytes, self.limit(), &mut pass);
        // The session's screen sees what the terminal sees.
        self.screen.feed(&pass);
        let mut at = 0;
        for end in std::mem::take(&mut self.scan.soft_resets) {
            self.mirror.feed(&pass[at..end]);
            self.mirror.feed(SOFT_RESET);
            at = end;
        }
        self.mirror.feed(&pass[at..]);
        out.extend_from_slice(&pass);
        let bar_hit = self.mirror.text(usize::from(self.rows - 1)) != self.bar_text();
        if bar_hit || self.scan.region_lost || self.pending || self.shown.is_some() {
            self.draw(&mut out);
        }
        out
    }

    /// The terminal changed size to `rows`×`cols` (it has to `fit`).
    pub fn resize(&mut self, rows: u16, cols: u16) -> Vec<u8> {
        self.rows = rows;
        self.cols = cols;
        self.screen.resize(rows - 1, cols);
        self.mirror.resize(rows, cols);
        // Terminals drop the margins when resized.
        self.scan.top = 1;
        self.scan.bottom = rows - 1;
        self.scan.region_lost = true;
        self.overlay = None;
        self.repaint = true;
        let mut out = Vec::new();
        self.draw(&mut out);
        out
    }

    /// New status; may open the overlay for a new approval.
    pub fn set_status(&mut self, status: Status) -> Vec<u8> {
        let fresh = status.approvals.iter().find(|a| !self.seen.contains(&a.id)).map(|a| a.id.clone());
        for a in &status.approvals {
            self.seen.insert(a.id.clone());
        }
        self.answered.retain(|id, at| {
            at.elapsed() < ANSWERED_FOR
                && !status.failed.contains(id)
                && status.approvals.iter().any(|a| &a.id == id)
        });
        self.status = status;
        if self.shown.as_ref().is_some_and(|id| !self.pending_approvals().any(|a| &a.id == id)) {
            self.shown = self.first_pending();
            self.armed = Instant::now() + ARMING;
        }
        if self.shown.is_none() && fresh.is_some() {
            self.shown = fresh;
            self.armed = Instant::now() + ARMING;
        }
        let mut out = Vec::new();
        self.draw(&mut out);
        out
    }

    fn pending_approvals(&self) -> impl Iterator<Item = &Approval> {
        self.status.approvals.iter().filter(|a| !self.answered.contains_key(&a.id))
    }

    fn first_pending(&self) -> Option<String> {
        self.pending_approvals().next().map(|a| a.id.clone())
    }

    /// Opens the overlay on the first approval waiting, if any.
    pub fn open_approvals(&mut self) -> Vec<u8> {
        self.shown = self.first_pending();
        self.armed = Instant::now();
        let mut out = Vec::new();
        self.draw(&mut out);
        out
    }

    /// Whether keys go to the overlay: it is on the screen.
    pub fn overlay_open(&self) -> bool {
        self.overlay.is_some()
    }

    /// Input read while the overlay is open: the bytes for the terminal, a
    /// decision if one was made, and input for the session (the terminal's
    /// replies, pastes, or everything when the overlay has closed meanwhile).
    pub fn key(&mut self, input: &[u8]) -> (Vec<u8>, Option<Decision>, Vec<u8>) {
        let Some(id) = self.shown.clone().filter(|_| self.overlay.is_some()) else {
            return (Vec::new(), None, input.to_vec());
        };
        // A paste spread over reads goes to the session to its end.
        if self.pasting {
            match input.windows(6).position(|w| w == b"\x1b[201~") {
                Some(p) => {
                    self.pasting = false;
                    let (mut out, decision, rest) = self.key(&input[p + 6..]);
                    let mut session = input[..p + 6].to_vec();
                    session.append(&mut out.split_off(0));
                    return (Vec::new(), decision, [session, rest].concat());
                }
                None => return (Vec::new(), None, input.to_vec()),
            }
        }
        let (keys, session) = split_input(input);
        if session.windows(6).any(|w| w == b"\x1b[200~") && !session.windows(6).any(|w| w == b"\x1b[201~") {
            self.pasting = true;
        }
        // Typed before the overlay could be read: meant for the session.
        if Instant::now() < self.armed {
            return (Vec::new(), None, input.to_vec());
        }
        // Exactly one key, pressed without modifiers; anything else typed
        // is dropped.
        let decision = match keys.as_slice() {
            [k] => match key(k) {
                Some(Key::Char('y')) => Some(true),
                Some(Key::Char('n')) => Some(false),
                Some(Key::Escape) => None,
                _ => return (Vec::new(), None, session),
            },
            _ => return (Vec::new(), None, session),
        };
        if decision.is_some() {
            self.answered.insert(id.clone(), Instant::now());
        }
        self.shown = match decision {
            Some(_) => self.first_pending(),
            None => None,
        };
        self.armed = Instant::now() + ARMING;
        let mut out = Vec::new();
        self.draw(&mut out);
        (out, decision.map(|d| (id, d)), session)
    }

    /// Gives the terminal back: `restore` (the session's modes turned off)
    /// is written first, then the scroll region and status line go.
    pub fn finish(&mut self, restore: &[u8]) -> Vec<u8> {
        let mut out = Vec::new();
        if self.overlay.take().is_some() {
            let saved = self.save();
            self.neutral(&saved, &mut out);
            self.sync_rows(&mut out);
            self.restore(&saved, &mut out);
        }
        self.emit(&mut out, restore);
        self.screen.feed(restore);
        let saved = self.save();
        let rows = self.rows;
        self.emit(&mut out, format!("\x1b[r\x1b[{rows};1H\x1b[0m\x1b[2K").as_bytes());
        self.scan.top = 1;
        self.restore(&saved, &mut out);
        out
    }

    fn bar_text(&self) -> String {
        let mut items = self.status.items.clone();
        match self.pending_approvals().count() {
            0 => {}
            1 => items.push("1 approval (ctrl-\\ a)".into()),
            n => items.push(format!("{n} approvals (ctrl-\\ a)")),
        }
        fit(&format!(" {}", items.join(" · ")), usize::from(self.cols))
    }

    /// What the terminal has now, before drawing.
    fn save(&self) -> Saved {
        self.save_from(true)
    }

    /// The cursor and modes of the terminal (`mirror`) or of the session.
    fn save_from(&self, mirror: bool) -> Saved {
        let from = if mirror { &self.mirror } else { &self.screen };
        let cursor = from.term.grid().cursor.clone();
        let wrap_cell = cursor
            .input_needs_wrap
            .then(|| from.cell(cursor.point.line.0 as usize, cursor.point.column.0).clone());
        Saved { cursor, mode: *from.term.mode(), wrap_cell }
    }

    /// Makes the terminal write plain text anywhere: no insert or origin
    /// mode, ASCII, no hyperlink.
    fn neutral(&mut self, saved: &Saved, out: &mut Vec<u8>) {
        let mut s = String::from("\x1b[0m\x1b]8;;\x1b\\\x1b(B\x0f");
        if saved.mode.contains(TermMode::INSERT) {
            s.push_str("\x1b[4l");
        }
        if saved.mode.contains(TermMode::ORIGIN) {
            s.push_str("\x1b[?6l");
        }
        self.emit(out, s.as_bytes());
    }

    /// Puts back what `save` found.
    fn restore(&mut self, saved: &Saved, out: &mut Vec<u8>) {
        let mut s = String::new();
        let (row, col) = (saved.cursor.point.line.0 as u16 + 1, saved.cursor.point.column.0 + 1);
        let origin = saved.mode.contains(TermMode::ORIGIN);
        let top = self.scan.top;
        let at = |row: u16, col: usize| {
            let row = if origin { row.saturating_sub(top - 1).max(1) } else { row };
            format!("\x1b[{row};{col}H")
        };
        if origin {
            s.push_str("\x1b[?6h");
        }
        match &saved.wrap_cell {
            // Writing the last cell again leaves the line waiting to wrap.
            Some(cell) if !cell.flags.contains(Flags::WIDE_CHAR_SPACER) => {
                s.push_str(&at(row, col));
                s.push_str(&sgr(cell));
                s.push_str(&link(cell));
                s.push(cell.c);
            }
            _ => s.push_str(&at(row, col)),
        }
        s.push_str(&sgr(&saved.cursor.template));
        s.push_str(&link(&saved.cursor.template));
        for (i, index) in [
            ('(', CharsetIndex::G0),
            (')', CharsetIndex::G1),
            ('*', CharsetIndex::G2),
            ('+', CharsetIndex::G3),
        ] {
            let set = match saved.cursor.charsets[index] {
                StandardCharset::Ascii => 'B',
                StandardCharset::SpecialCharacterAndLineDrawing => '0',
            };
            if set != 'B' || index == CharsetIndex::G0 {
                s.push_str(&format!("\x1b{i}{set}"));
            }
        }
        s.push_str(match self.scan.gl {
            1 => "\x0e",
            2 => "\x1bn",
            3 => "\x1bo",
            _ => "",
        });
        if saved.mode.contains(TermMode::INSERT) {
            s.push_str("\x1b[4h");
        }
        self.emit(out, s.as_bytes());
    }

    /// Repaints the cells of the session's rows where the terminal differs
    /// from the session's screen (an overlay, or a status line left by a
    /// resize), with a column more on each side for wide characters.
    fn sync_rows(&mut self, out: &mut Vec<u8>) {
        let cols = self.screen.cols().min(self.mirror.cols());
        for row in 0..usize::from(self.limit()) {
            let differs = |c: &usize| !same_cell(self.mirror.cell(row, *c), self.screen.cell(row, *c));
            let Some(first) = (0..cols).find(differs) else { continue };
            let last = (0..cols).rev().find(differs).unwrap_or(first);
            let (from, to) = (first.saturating_sub(1), (last + 2).min(cols));
            // Not from inside a wide character.
            let from = if self.screen.cell(row, from).flags.contains(Flags::WIDE_CHAR_SPACER) {
                from.saturating_sub(1)
            } else {
                from
            };
            let mut bytes = Vec::new();
            write_row(&self.screen, row, from, to, &mut bytes);
            self.emit(out, &bytes);
        }
    }

    /// Draws the status line and the overlay where they are not right, then
    /// puts the terminal's state back. Waits while the output is inside a
    /// sequence, a string or a character.
    fn draw(&mut self, out: &mut Vec<u8>) {
        if !self.scan.at_rest() {
            self.pending = true;
            return;
        }
        self.pending = false;
        // After a resize the cursor goes where the session has it.
        let saved = if self.repaint { self.save_from(false) } else { self.save_from(true) };
        self.neutral(&saved, out);
        if self.scan.region_lost {
            let region = format!("\x1b[{};{}r", self.scan.top, self.scan.bottom);
            self.emit(out, region.as_bytes());
            self.scan.region_lost = false;
        }
        let lines = self.overlay_lines();
        if self.repaint {
            self.repaint = false;
            let cols = self.screen.cols();
            for row in 0..usize::from(self.limit()) {
                let mut bytes = Vec::new();
                write_row(&self.screen, row, 0, cols, &mut bytes);
                self.emit(out, &bytes);
            }
            let rows = self.rows;
            self.emit(out, format!("\x1b[{rows};1H\x1b[2K").as_bytes());
        } else if self.overlay.as_ref().map(|(_, _, l)| l) != lines.as_ref().map(|(_, _, l)| l) {
            self.overlay = None;
            self.sync_rows(out);
        }
        let bar = self.bar_text();
        if self.mirror.text(usize::from(self.rows - 1)) != bar {
            let rows = self.rows;
            self.emit(out, format!("\x1b[{rows};1H\x1b#5{BAR_STYLE}{bar}\x1b[0m").as_bytes());
        }
        if let Some((top, left, lines)) = lines {
            for (i, line) in lines.iter().enumerate() {
                let row = usize::from(top) - 1 + i;
                let have: String = (0..width(line))
                    .map(|c| self.mirror.cell(row, usize::from(left) - 1 + c))
                    .filter(|c| !c.flags.contains(Flags::WIDE_CHAR_SPACER))
                    .map(|c| c.c)
                    .collect();
                if have != *line {
                    let r = top + i as u16;
                    let style = if i == 0 { OVERLAY_TITLE } else { OVERLAY_STYLE };
                    self.emit(out, format!("\x1b[{r};{left}H\x1b#5{style}{line}\x1b[0m").as_bytes());
                }
            }
            self.overlay = Some((top, left, lines));
        }
        self.restore(&saved, out);
    }

    /// The overlay for the approval shown: its first row, column and lines.
    fn overlay_lines(&mut self) -> Option<(u16, u16, Vec<String>)> {
        let Some(a) =
            self.shown.as_ref().and_then(|id| self.status.approvals.iter().find(|a| &a.id == id)).cloned()
        else {
            self.shown = None;
            return None;
        };
        let w = self.cols.saturating_sub(4).min(76);
        let inner = usize::from(w - 4);
        // The summary comes first; the detail gets what room is left.
        let room = usize::from(self.limit()).saturating_sub(4);
        let summary = at_most(wrap(&a.summary, inner), room.max(1), inner);
        let mut lines = summary.clone();
        let left_over = room.saturating_sub(summary.len() + 1).min(4);
        if !a.detail.is_empty() && left_over > 0 {
            lines.push(String::new());
            lines.extend(at_most(wrap(&a.detail, inner), left_over, inner));
        }
        lines.push(String::new());
        lines.push("y approve · n deny · esc later".into());
        let count = self.pending_approvals().count();
        let index = self.pending_approvals().position(|p| p.id == a.id).unwrap_or(0) + 1;
        let tail = if count > 1 { format!(" {index}/{count} ") } else { String::new() };
        let title = fit(&format!(" {} ", a.kind), usize::from(w - 3).saturating_sub(width(&tail)));
        let title = title.trim_end();
        let fill = usize::from(w - 2).saturating_sub(width(title) + width(&tail) + 1);
        let mut rows = vec![format!("┌─{title} {}{tail}┐", "─".repeat(fill.saturating_sub(1)))];
        rows.extend(lines.iter().map(|l| format!("│ {} │", fit(l, inner))));
        rows.push(format!("└{}┘", "─".repeat(usize::from(w - 2))));
        if rows.len() > usize::from(self.limit()) {
            return None;
        }
        let top = self.limit() + 1 - rows.len() as u16;
        let left = (self.cols - w) / 2 + 1;
        Some((top, left, rows))
    }
}

/// Splits input read while the overlay is open into keys and what goes to
/// the session: the terminal's replies (reports, colors, focus, mouse) and
/// pastes.
fn split_input(input: &[u8]) -> (Vec<Vec<u8>>, Vec<u8>) {
    let mut keys = Vec::new();
    let mut session = Vec::new();
    let mut i = 0;
    while i < input.len() {
        let rest = &input[i..];
        let (len, reply) = if rest.starts_with(b"\x1b[200~") {
            let end = rest.windows(6).position(|w| w == b"\x1b[201~").map(|p| p + 6).unwrap_or(rest.len());
            (end, true)
        } else if rest.starts_with(b"\x1b[") {
            let end =
                rest[2..].iter().position(|b| (0x40..=0x7e).contains(b)).map(|p| p + 3).unwrap_or(rest.len());
            let fin = rest[end - 1];
            let private = rest.get(2).copied();
            let reply = matches!(fin, b'R' | b'c' | b'n' | b't' | b'y' | b'I' | b'O' | b'M' | b'm')
                || (fin == b'u' && private == Some(b'?'))
                || private == Some(b'<');
            (end, reply)
        } else if rest.starts_with(b"\x1b]") || rest.starts_with(b"\x1bP") || rest.starts_with(b"\x1b_") {
            let st = rest.windows(2).position(|w| w == b"\x1b\\").map(|p| p + 2);
            let bel = rest.iter().position(|b| *b == 0x07).map(|p| p + 1);
            (st.into_iter().chain(bel).min().unwrap_or(rest.len()), true)
        } else if rest.starts_with(b"\x1bO") && rest.len() >= 3 {
            (3, false)
        } else if rest[0] == 0x1b && rest.len() >= 2 {
            // Alt with a key.
            (2, false)
        } else {
            let n = (1..=rest.len().min(4)).find(|n| std::str::from_utf8(&rest[..*n]).is_ok()).unwrap_or(1);
            (n, false)
        };
        if reply {
            session.extend_from_slice(&rest[..len]);
        } else {
            keys.push(rest[..len].to_vec());
        }
        i += len;
    }
    (keys, session)
}

/// A key the overlay understands.
#[derive(Debug, PartialEq, Eq)]
enum Key {
    Char(char),
    Escape,
}

/// `input` as one key pressed without modifiers: a plain character, Escape,
/// or the same in the kitty keyboard protocol (`CSI code [; mods[:event]] u`).
fn key(input: &[u8]) -> Option<Key> {
    if input == b"\x1b" {
        return Some(Key::Escape);
    }
    if let Some(rest) = input.strip_prefix(b"\x1b[").and_then(|r| r.strip_suffix(b"u")) {
        let text = std::str::from_utf8(rest).ok()?;
        let mut parts = text.split(';');
        let code: u32 = parts.next()?.split(':').next()?.parse().ok()?;
        if let Some(mods) = parts.next() {
            let mut m = mods.split(':');
            let modifiers: u32 = m.next()?.parse().ok()?;
            let event: u32 = match m.next() {
                Some(e) => e.parse().ok()?,
                None => 1,
            };
            // Caps Lock and Num Lock are not modifiers here.
            if (modifiers.saturating_sub(1) & !(64 | 128)) != 0 || event != 1 {
                return None;
            }
        }
        return match code {
            27 => Some(Key::Escape),
            c => char::from_u32(c).filter(|c| !c.is_control()).map(Key::Char),
        };
    }
    let mut chars = std::str::from_utf8(input).ok()?.chars();
    let c = chars.next()?;
    (chars.next().is_none() && !c.is_control()).then_some(Key::Char(c))
}

#[cfg(test)]
mod tests {
    use super::*;

    /// A terminal that shows what the compositor writes.
    struct Terminal {
        vt: vt100::Parser,
        comp: Compositor,
    }

    impl Terminal {
        fn new(rows: u16, cols: u16) -> Terminal {
            let mut t = Terminal { vt: vt100::Parser::new(rows, cols, 0), comp: Compositor::new(rows, cols) };
            let start = t.comp.start(Some((1, 1)));
            t.vt.process(&start);
            t
        }

        fn session(&mut self, bytes: &[u8]) {
            let out = self.comp.output(bytes);
            self.vt.process(&out);
        }

        fn apply(&mut self, out: Vec<u8>) {
            self.vt.process(&out);
        }

        fn status(&mut self, s: Status) {
            let out = self.comp.set_status(s);
            self.apply(out);
        }

        /// A key, once the overlay takes keys.
        fn key(&mut self, k: &[u8]) -> Option<Decision> {
            self.comp.armed = Instant::now();
            let (out, decision, _) = self.comp.key(k);
            self.apply(out);
            decision
        }

        fn row(&self, r: u16) -> String {
            let cols = self.vt.screen().size().1;
            self.vt.screen().contents_between(r, 0, r, cols).trim_end().to_string()
        }

        /// The terminal shows the session's screen above the status line,
        /// with the cursor where the session has it.
        fn matches_session(&self) {
            let (rows, _) = self.comp.session_size();
            for r in 0..rows {
                assert_eq!(self.row(r), self.comp.screen.text(r.into()).trim_end(), "row {r}");
            }
            let cursor = self.comp.screen.term.grid().cursor.point;
            assert_eq!(self.vt.screen().cursor_position(), (cursor.line.0 as u16, cursor.column.0 as u16));
        }
    }

    fn status(approvals: &[(&str, &str)]) -> Status {
        Status {
            items: vec!["m1".into(), "dev/arch".into()],
            approvals: approvals
                .iter()
                .map(|(id, summary)| Approval {
                    id: id.to_string(),
                    kind: "git.push".into(),
                    summary: summary.to_string(),
                    detail: String::new(),
                })
                .collect(),
            failed: Vec::new(),
        }
    }

    #[test]
    fn output_scrolls_above_the_status_line() {
        let mut t = Terminal::new(6, 40);
        t.status(status(&[]));
        for i in 0..20 {
            t.session(format!("line {i}\r\n").as_bytes());
        }
        t.matches_session();
        assert_eq!(t.row(5), " m1 · dev/arch");
        assert_eq!(t.row(3), "line 19");
    }

    #[test]
    fn the_session_cannot_reach_the_status_line() {
        let mut t = Terminal::new(6, 40);
        t.status(status(&[]));
        // A full-screen program: its own region, a clear, rows past its end,
        // a sequence broken by another escape.
        t.session(b"\x1b[?1049h\x1b[r\x1b[2J\x1b[99;1Hbottom\x1b[1;99rx\x1b[H\x1b[Jtop\x1b[1\x1b[99;1Hy");
        t.matches_session();
        assert_eq!(t.row(5), " m1 · dev/arch");
        t.session(b"\x1b[?1049l");
        t.matches_session();
        assert_eq!(t.row(5), " m1 · dev/arch");
    }

    #[test]
    fn nothing_is_drawn_inside_strings_or_characters() {
        let mut t = Terminal::new(6, 40);
        t.status(status(&[]));
        // A clear that takes the status line, then a hyperlink and a
        // character split across chunks.
        let out = t.comp.output(b"\x1b[2J\x1b]8;;http://x/");
        assert!(out.ends_with(b"http://x/"), "drawn inside the string");
        t.apply(out);
        t.session(b"\x1b\\link\xe2\x94");
        t.session(b"\x80");
        t.matches_session();
        assert_eq!(t.row(5), " m1 · dev/arch");
        assert!(t.row(0).contains("link─"));
    }

    #[test]
    fn approvals_open_as_overlays_and_leave_no_trace() {
        let mut t = Terminal::new(12, 50);
        for i in 0..11 {
            t.session(format!("\x1b[3{}mline {i}\x1b[0m\r\n", i % 7).as_bytes());
        }
        t.session(b"\x1b[1mbold");
        t.status(status(&[("a1", "git push main to origin")]));
        assert!(t.comp.overlay_open());
        let screen: String = (0..11).map(|r| t.row(r) + "\n").collect();
        assert!(screen.contains("git push main to origin"), "{screen}");
        assert!(screen.contains("y approve"), "{screen}");
        assert!(t.row(11).contains("1 approval"));
        // Output goes on under the overlay; the overlay does not scroll.
        t.session(b"\r\nmore\r\nand more\r\n");
        let screen: String = (0..11).map(|r| t.row(r) + "\n").collect();
        assert_eq!(screen.matches("y approve").count(), 1, "{screen}");
        assert_eq!(t.key(b"y"), Some(("a1".into(), true)));
        assert!(!t.comp.overlay_open());
        t.matches_session();
        // Attributes are the session's again.
        t.session(b"x");
        let (row, col) = t.vt.screen().cursor_position();
        assert!(t.vt.screen().cell(row, col - 1).unwrap().bold());
    }

    #[test]
    fn keys_right_after_the_overlay_opens_are_not_decisions() {
        let mut t = Terminal::new(12, 50);
        t.status(status(&[("a1", "one")]));
        let (_, decision, forward) = t.comp.key(b"y");
        assert_eq!(decision, None, "typed before the overlay could be read");
        assert_eq!(forward, b"y", "and so the session's");
        t.comp.armed = Instant::now();
        // More than one key, a modifier or a release decides nothing.
        for input in [&b"yes"[..], b"\x1b[121;5u", b"\x1b[121;1:3u"] {
            assert_eq!(t.comp.key(input).1, None, "{input:?}");
        }
        // The terminal's replies go on to the session.
        let (_, decision, forward) = t.comp.key(b"\x1b[12;40R");
        assert_eq!((decision, forward), (None, b"\x1b[12;40R".to_vec()));
        assert_eq!(t.key(b"\x1b[121u"), Some(("a1".into(), true)));
    }

    #[test]
    fn a_paste_over_reads_stays_the_sessions() {
        let mut t = Terminal::new(12, 50);
        t.status(status(&[("a1", "one")]));
        t.comp.armed = Instant::now();
        assert_eq!(t.comp.key(b"\x1b[200~first").2, b"\x1b[200~first");
        assert_eq!(t.comp.key(b"y").2, b"y", "still pasting");
        let (_, decision, forward) = t.comp.key(b"end\x1b[201~");
        assert_eq!((decision, forward), (None, b"end\x1b[201~".to_vec()));
        assert_eq!(t.key(b"y"), Some(("a1".into(), true)));
    }

    #[test]
    fn malformed_moves_and_overlong_sequences_do_not_pass() {
        let mut out = Vec::new();
        let mut scan = Scan::new(10);
        scan.feed(b"\x1b[99:1Ha\x1b[9\x7f9;1Hb\x1b(\x1b[99;1Hc", 10, &mut out);
        assert_eq!(out, b"a\x1b[10;1Hb\x1b(\x1b[10;1Hc");
        let mut out = Vec::new();
        let long = [b"\x1b[".to_vec(), vec![b'0'; 300], b"24;1Hd".to_vec()].concat();
        scan.feed(&long, 10, &mut out);
        assert_eq!(out, b"d");
        assert!(scan.sequence.capacity() < 1024);
    }

    #[test]
    fn a_soft_reset_reaches_the_mirror() {
        let mut t = Terminal::new(12, 50);
        t.session(b"\x1b[4h\x1b(0x\x1b[!p");
        let mode = *t.comp.mirror.term.mode();
        assert!(!mode.contains(TermMode::INSERT));
        assert_eq!(t.comp.mirror.term.grid().cursor.charsets[CharsetIndex::G0], StandardCharset::Ascii);
    }

    #[test]
    fn a_dismissed_approval_waits_for_the_key() {
        let mut t = Terminal::new(12, 50);
        t.status(status(&[("a1", "one")]));
        assert_eq!(t.key(b"\x1b"), None);
        assert!(!t.comp.overlay_open());
        // The same approval does not open again by itself; a new one does.
        t.status(status(&[("a1", "one")]));
        assert!(!t.comp.overlay_open());
        let out = t.comp.open_approvals();
        t.apply(out);
        assert!(t.comp.overlay_open());
        assert_eq!(t.key(b"\x1b[110;1u"), Some(("a1".into(), false)));
        t.matches_session();
    }

    #[test]
    fn origin_mode_charsets_and_insert_mode_are_kept() {
        let mut t = Terminal::new(12, 50);
        t.session(b"\x1b[3;8r\x1b[?6h\x1b[2;5H\x1b(0\x1b[4h");
        t.status(status(&[("a1", "one")]));
        assert!(t.comp.overlay_open(), "drawn in origin mode too");
        t.session(b"q");
        t.key(b"\x1b");
        let mirror = &t.comp.mirror.term;
        assert!(mirror.mode().contains(TermMode::ORIGIN));
        assert!(mirror.mode().contains(TermMode::INSERT));
        assert_eq!(
            mirror.grid().cursor.charsets[CharsetIndex::G0],
            StandardCharset::SpecialCharacterAndLineDrawing
        );
        let (s, m) = (t.comp.screen.term.grid().cursor.point, mirror.grid().cursor.point);
        assert_eq!((s.line.0, s.column.0), (m.line.0, m.column.0));
    }

    #[test]
    fn resizing_keeps_the_status_line_and_the_region() {
        let mut t = Terminal::new(8, 40);
        t.status(status(&[]));
        t.session(b"hello");
        t.vt.screen_mut().set_size(10, 50);
        let out = t.comp.resize(10, 50);
        t.apply(out);
        assert_eq!(t.comp.session_size(), (9, 50));
        assert_eq!(t.row(9), " m1 · dev/arch");
        for i in 0..20 {
            t.session(format!("\r\nline {i}").as_bytes());
        }
        assert_eq!(t.row(8), "line 19", "the whole session area scrolls");
        assert_eq!(t.row(9), " m1 · dev/arch");
        // Growing leaves no old status line in the session's rows.
        t.vt.screen_mut().set_size(12, 50);
        let out = t.comp.resize(12, 50);
        t.apply(out);
        let screen: String = (0..11).map(|r| t.row(r) + "\n").collect();
        assert!(!screen.contains("dev/arch"), "{screen}");
    }

    #[test]
    fn finishing_gives_the_whole_terminal_back() {
        let mut t = Terminal::new(6, 40);
        t.status(status(&[]));
        t.session(b"a\r\nb");
        let out = t.comp.finish(b"\x1b[0m");
        t.apply(out);
        assert_eq!(t.row(5), "");
        for _ in 0..10 {
            t.vt.process(b"\r\nx");
        }
        assert_eq!(t.row(5), "x", "the last row scrolls again");
    }

    #[test]
    fn keys_in_both_encodings() {
        assert_eq!(key(b"y"), Some(Key::Char('y')));
        assert_eq!(key(b"\x1b"), Some(Key::Escape));
        assert_eq!(key(b"\x1b[27u"), Some(Key::Escape));
        assert_eq!(key(b"\x1b[121;1u"), Some(Key::Char('y')));
        assert_eq!(key(b"\x1b[121;1:2u"), None);
        assert_eq!(key(b"\x1b[121;3u"), None);
        assert_eq!(key(b"\x1b[121;65u"), Some(Key::Char('y')), "Caps Lock is not a modifier");
        assert_eq!(key(b"yy"), None);
        let (keys, session) = split_input(b"y\x1b[200~pasted\x1b[201~\x1b[<0;1;2M\x1b]11;rgb:0/0/0\x1b\\");
        assert_eq!(keys, vec![b"y".to_vec()]);
        assert_eq!(session, b"\x1b[200~pasted\x1b[201~\x1b[<0;1;2M\x1b]11;rgb:0/0/0\x1b\\".to_vec());
    }

    #[test]
    fn long_text_is_cut_visibly() {
        assert_eq!(wrap("git push main to origin", 10), ["git push", "main to", "origin"]);
        assert_eq!(wrap("abcdefghijkl", 5), ["abcde", "fghij", "kl"]);
        assert_eq!(at_most(wrap("a b c d e f", 3), 2, 3), ["a b", "c…"]);
        assert_eq!(fit("ab", 4), "ab  ");
        assert_eq!(fit("日本語", 4), "日本");
    }
}
