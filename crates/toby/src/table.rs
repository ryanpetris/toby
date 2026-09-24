//! Column output for list commands.

/// Prints rows under a header with columns sized to their contents.
pub fn print<const N: usize>(header: [&str; N], rows: Vec<[String; N]>) {
    let mut widths = header.map(str::len);
    for row in &rows {
        for (w, cell) in widths.iter_mut().zip(row) {
            *w = (*w).max(cell.chars().count());
        }
    }
    let line = |cells: [&str; N]| {
        let mut out = String::new();
        for (i, (cell, w)) in cells.iter().zip(widths).enumerate() {
            if i + 1 == N {
                out.push_str(cell);
            } else {
                out.push_str(&format!("{cell:<w$}  "));
            }
        }
        println!("{}", out.trim_end());
    };
    line(header);
    for row in &rows {
        line(row.each_ref().map(String::as_str));
    }
}

/// How long ago a Unix time was, roughly.
pub fn age(created: u64) -> String {
    duration(toby_store::records::now().saturating_sub(created)) + " ago"
}

pub fn duration(secs: u64) -> String {
    match secs {
        s if s < 60 => format!("{s} seconds"),
        s if s < 3600 => format!("{} minutes", s / 60),
        s if s < 86400 => format!("{} hours", s / 3600),
        s => format!("{} days", s / 86400),
    }
}
