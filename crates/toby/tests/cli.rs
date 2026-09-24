//! Commands that run without a daemon.

use std::path::Path;
use std::process::Command;

const BIN: &str = env!("CARGO_BIN_EXE_toby");

#[test]
fn config_get_and_set() {
    let home = tempdir();
    let toby = |args: &[&str]| Command::new(BIN).args(args).env("HOME", &home).output().unwrap();
    let out = toby(&["config", "get", "daemon.backend"]);
    assert_eq!(out.status.code(), Some(1));
    assert!(String::from_utf8_lossy(&out.stderr).contains("is not set"), "{out:?}");
    assert!(toby(&["config", "set", "daemon.backend", "direct"]).status.success());
    let out = toby(&["config", "get", "daemon.backend"]);
    assert_eq!(String::from_utf8_lossy(&out.stdout), "direct\n");
    let out = toby(&["config", "set", "daemon.backend", "nonsense"]);
    assert_eq!(out.status.code(), Some(1));
    std::fs::remove_dir_all(home).unwrap();
}

#[test]
fn multicall_names_dispatch() {
    let dir = tempdir();
    // toby-connect is the guest's MCP bridge; outside a machine it has no
    // sandbox socket to connect to.
    let link = dir.join("toby-connect");
    std::os::unix::fs::symlink(BIN, &link).unwrap();
    let out = Command::new(&link).arg("mcp/toby").output().unwrap();
    assert_eq!(out.status.code(), Some(1));
    assert!(String::from_utf8_lossy(&out.stderr).contains("sandbox.sock"), "{out:?}");

    // tobyd is the daemon; its help shows the dispatch without starting it.
    let link = dir.join("tobyd");
    std::os::unix::fs::symlink(BIN, &link).unwrap();
    let out = Command::new(&link).arg("--help").output().unwrap();
    assert_eq!(out.status.code(), Some(0));
    assert!(String::from_utf8_lossy(&out.stdout).contains("daemon"), "{out:?}");
    std::fs::remove_dir_all(dir).unwrap();
}

#[test]
fn tool_help_and_errors_come_from_the_parser() {
    let out = Command::new(BIN).args(["claude", "--help"]).output().unwrap();
    assert_eq!(out.status.code(), Some(0));
    assert!(String::from_utf8_lossy(&out.stdout).contains("Usage: toby <tool>"));

    let out = Command::new(BIN).args(["claude", "--no-such-flag"]).output().unwrap();
    assert_eq!(out.status.code(), Some(2));
}

#[test]
fn internal_and_guest_commands_are_hidden() {
    let out = Command::new(BIN).arg("--help").output().unwrap();
    let help = String::from_utf8_lossy(&out.stdout);

    assert!(!help.contains("internal"));
    assert!(!help.contains("guest"));
}

fn tempdir() -> std::path::PathBuf {
    static N: std::sync::atomic::AtomicU32 = std::sync::atomic::AtomicU32::new(0);
    let n = N.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
    let dir = Path::new(env!("CARGO_TARGET_TMPDIR")).join(format!("cli-{}-{n}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(&dir).unwrap();
    dir
}
