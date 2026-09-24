//! The systemd user manager and logind over D-Bus (plan §12.2).

use std::io;

use zbus::zvariant::OwnedObjectPath;

#[zbus::proxy(
    interface = "org.freedesktop.systemd1.Manager",
    default_service = "org.freedesktop.systemd1",
    default_path = "/org/freedesktop/systemd1"
)]
trait Manager {
    fn start_unit(&self, name: &str, mode: &str) -> zbus::Result<OwnedObjectPath>;
    fn stop_unit(&self, name: &str, mode: &str) -> zbus::Result<OwnedObjectPath>;
    fn restart_unit(&self, name: &str, mode: &str) -> zbus::Result<OwnedObjectPath>;
    fn load_unit(&self, name: &str) -> zbus::Result<OwnedObjectPath>;
    fn reset_failed_unit(&self, name: &str) -> zbus::Result<()>;
}

#[zbus::proxy(interface = "org.freedesktop.systemd1.Unit", default_service = "org.freedesktop.systemd1")]
trait Unit {
    #[zbus(property)]
    fn active_state(&self) -> zbus::Result<String>;
    #[zbus(property)]
    fn load_state(&self) -> zbus::Result<String>;
    /// The unit's pending job, `(0, "/")` when there is none.
    #[zbus(property)]
    fn job(&self) -> zbus::Result<(u32, OwnedObjectPath)>;
}

#[zbus::proxy(
    interface = "org.freedesktop.login1.Manager",
    default_service = "org.freedesktop.login1",
    default_path = "/org/freedesktop/login1"
)]
trait Login {
    fn get_user(&self, uid: u32) -> zbus::Result<OwnedObjectPath>;
    fn set_user_linger(&self, uid: u32, enable: bool, interactive: bool) -> zbus::Result<()>;
    #[zbus(property)]
    fn kill_user_processes(&self) -> zbus::Result<bool>;
}

#[zbus::proxy(interface = "org.freedesktop.login1.User", default_service = "org.freedesktop.login1")]
trait LoginUser {
    #[zbus(property)]
    fn linger(&self) -> zbus::Result<bool>;
}

fn err(e: zbus::Error) -> io::Error {
    io::Error::other(e.to_string())
}

/// The user's systemd instance.
#[derive(Clone)]
pub struct SystemdUser {
    bus: zbus::Connection,
}

impl SystemdUser {
    pub async fn connect() -> io::Result<SystemdUser> {
        let bus = zbus::Connection::session()
            .await
            .map_err(|e| io::Error::other(format!("cannot reach the systemd user instance: {e}")))?;
        Ok(SystemdUser { bus })
    }

    async fn manager(&self) -> io::Result<ManagerProxy<'_>> {
        ManagerProxy::new(&self.bus).await.map_err(err)
    }

    pub async fn start(&self, unit: &str) -> io::Result<()> {
        self.manager().await?.start_unit(unit, "replace").await.map(drop).map_err(err)
    }

    pub async fn stop(&self, unit: &str) -> io::Result<()> {
        self.manager().await?.stop_unit(unit, "replace").await.map(drop).map_err(err)
    }

    pub async fn reset_failed(&self, unit: &str) -> io::Result<()> {
        self.manager().await?.reset_failed_unit(unit).await.map_err(err)
    }

    pub async fn restart(&self, unit: &str) -> io::Result<()> {
        self.manager().await?.restart_unit(unit, "replace").await.map(drop).map_err(err)
    }

    async fn unit(&self, unit: &str) -> io::Result<UnitProxy<'_>> {
        let path = self.manager().await?.load_unit(unit).await.map_err(err)?;
        UnitProxy::builder(&self.bus).path(path).map_err(err)?.build().await.map_err(err)
    }

    /// The unit's active state (`active`, `inactive`, `failed`, …), or
    /// `not-found` when no such unit file exists.
    pub async fn state(&self, unit: &str) -> io::Result<String> {
        let proxy = self.unit(unit).await?;
        if proxy.load_state().await.map_err(err)? == "not-found" {
            return Ok("not-found".into());
        }
        proxy.active_state().await.map_err(err)
    }

    /// Whether the unit runs or has a job queued (a start waiting for its
    /// dependencies still shows as inactive).
    pub async fn busy(&self, unit: &str) -> io::Result<bool> {
        let proxy = self.unit(unit).await?;
        let state = proxy.active_state().await.map_err(err)?;
        if matches!(state.as_str(), "active" | "activating" | "deactivating" | "reloading") {
            return Ok(true);
        }
        Ok(proxy.job().await.map_err(err)?.0 != 0)
    }
}

/// Whether logind keeps the user's manager running after logout.
pub async fn linger(uid: u32) -> io::Result<bool> {
    let bus = zbus::Connection::system().await.map_err(err)?;
    let path = LoginProxy::new(&bus).await.map_err(err)?.get_user(uid).await.map_err(err)?;
    let user = LoginUserProxy::builder(&bus).path(path).map_err(err)?.build().await.map_err(err)?;
    user.linger().await.map_err(err)
}

/// Whether logind ends a user's processes when the user logs out.
pub async fn kill_user_processes() -> io::Result<bool> {
    let bus = zbus::Connection::system().await.map_err(err)?;
    LoginProxy::new(&bus).await.map_err(err)?.kill_user_processes().await.map_err(err)
}

pub async fn set_linger(uid: u32, enable: bool) -> io::Result<()> {
    let bus = zbus::Connection::system().await.map_err(err)?;
    LoginProxy::new(&bus).await.map_err(err)?.set_user_linger(uid, enable, false).await.map_err(err)
}
