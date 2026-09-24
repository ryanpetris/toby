//! Every subcommand is wired and reports that it is not implemented yet.

use std::path::Path;
use std::process::Command;

const BIN: &str = env!("CARGO_BIN_EXE_toby");

const INVOCATIONS: &[&[&str]] = &[
    &["run", "-f", "launch.toml"],
    &["config", "get", "daemon.backend"],
    &["config", "set", "daemon.backend", "direct"],
];

fn assert_stub(mut cmd: Command, label: &str) {
    let out = cmd.output().expect("run toby");
    let stderr = String::from_utf8_lossy(&out.stderr);

    assert_eq!(out.status.code(), Some(1), "{label}: {stderr}");
    assert!(stderr.contains("is not implemented yet"), "{label}: {stderr}");
}

#[test]
fn every_subcommand_runs() {
    for args in INVOCATIONS {
        let mut cmd = Command::new(BIN);
        cmd.args(*args);
        assert_stub(cmd, &args.join(" "));
    }
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
    let dir = Path::new(env!("CARGO_TARGET_TMPDIR")).join(format!("multicall-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(&dir).unwrap();
    dir
}
