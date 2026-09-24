//! A project's `.toby/config.toml` and launch files (plan §14.3, §14.6):
//! what one launch of a tool uses, over the global configuration.

use std::collections::BTreeMap;
use std::io;
use std::path::Path;

use serde::Deserialize;

/// Where a root's image comes from.
#[derive(Debug, Clone, PartialEq, Eq, Deserialize)]
#[serde(untagged, deny_unknown_fields)]
pub enum ImageConfig {
    /// `"default"`: the default image.
    Named(String),
    Mkosi {
        mkosi: String,
    },
    Dockerfile {
        dockerfile: String,
        context: Option<String>,
    },
    Registry {
        registry: String,
    },
    Archive {
        archive: String,
    },
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ProjectEntry {
    /// The project's directory; by default `<projects_dir>/<name>`.
    pub path: Option<String>,
    /// The project the tool starts in.
    #[serde(default)]
    pub primary: bool,
}

/// A forward while the launch's session runs.
#[derive(Debug, Clone, PartialEq, Eq, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ForwardEntry {
    /// `host-to-guest` (the default) or `guest-to-host`.
    #[serde(default = "host_to_guest")]
    pub direction: String,
    pub host: u16,
    pub guest: Option<u16>,
}

fn host_to_guest() -> String {
    "host-to-guest".into()
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct LaunchSettings {
    pub yolo: Option<bool>,
}

/// A launch file, or a project's configuration (which may not name tools,
/// parameters or settings).
#[derive(Debug, Clone, Default, PartialEq, Eq, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Launch {
    /// The tool to start.
    pub tool: Option<String>,
    /// Other tools prepared in the machine too.
    #[serde(default)]
    pub tools: Vec<String>,
    /// Arguments for the tool.
    #[serde(default)]
    pub params: Vec<String>,
    pub home: Option<String>,
    pub root: Option<String>,
    /// The root's image, used when the root is created.
    pub image: Option<ImageConfig>,
    #[serde(default)]
    pub projects: BTreeMap<String, ProjectEntry>,
    /// Where the tool starts in the machine; `~` is the home there.
    pub workdir: Option<String>,
    #[serde(default)]
    pub forwards: Vec<ForwardEntry>,
    /// Configured MCP servers the tool gets for this launch.
    #[serde(default)]
    pub mcp: Vec<String>,
    pub cpus: Option<u32>,
    pub memory: Option<String>,
    #[serde(default)]
    pub settings: LaunchSettings,
}

fn invalid(msg: String) -> io::Error {
    io::Error::new(io::ErrorKind::InvalidData, msg)
}

impl Launch {
    /// Reads a launch file.
    pub fn load(path: &Path) -> io::Result<Launch> {
        let text = std::fs::read_to_string(path)
            .map_err(|e| io::Error::new(e.kind(), format!("{}: {e}", path.display())))?;
        toml::from_str(&text).map_err(|e| invalid(format!("{}: {e}", path.display())))
    }

    /// Reads a project's `.toby/config.toml`, which may not name tools,
    /// parameters or settings, nor use substitutions.
    pub fn load_project(path: &Path) -> io::Result<Launch> {
        let text = std::fs::read_to_string(path)
            .map_err(|e| io::Error::new(e.kind(), format!("{}: {e}", path.display())))?;
        if text.contains("{file:") || text.contains("{env:") {
            return Err(invalid(format!(
                "{}: a project's configuration cannot use substitutions",
                path.display()
            )));
        }
        let l: Launch = toml::from_str(&text).map_err(|e| invalid(format!("{}: {e}", path.display())))?;
        if l.tool.is_some()
            || !l.tools.is_empty()
            || !l.params.is_empty()
            || l.settings != LaunchSettings::default()
        {
            return Err(invalid(format!(
                "{}: a project's configuration cannot choose tools, parameters or settings; a launch file can",
                path.display()
            )));
        }
        Ok(l)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn launch_files_and_project_configs() {
        let l: Launch = toml::from_str(
            "tool = \"claude\"\nparams = [\"--model\", \"opus\"]\nhome = \"work\"\n\
             image = { dockerfile = \".toby/Dockerfile\", context = \".\" }\n\
             forwards = [{ host = 3000 }]\n[projects.app]\nprimary = true\n[projects.lib]\npath = \"../lib\"\n\
             [settings]\nyolo = true\n",
        )
        .unwrap();
        assert_eq!(l.tool.as_deref(), Some("claude"));
        assert_eq!(
            l.image,
            Some(ImageConfig::Dockerfile {
                dockerfile: ".toby/Dockerfile".into(),
                context: Some(".".into())
            })
        );
        assert_eq!(l.forwards[0].direction, "host-to-guest");
        assert!(l.projects["app"].primary);
        assert_eq!(l.settings.yolo, Some(true));
        assert!(
            toml::from_str::<Launch>("image = \"default\"\n").unwrap().image
                == Some(ImageConfig::Named("default".into()))
        );
        assert!(toml::from_str::<Launch>("unknown = 1\n").is_err());

        let dir = tempfile::tempdir().unwrap();
        let p = dir.path().join("config.toml");
        std::fs::write(&p, "tool = \"claude\"\n").unwrap();
        assert!(Launch::load_project(&p).is_err());
        std::fs::write(&p, "mcp = [\"github\"]\nhome = \"{env:HOME}\"\n").unwrap();
        assert!(Launch::load_project(&p).is_err());
        std::fs::write(&p, "mcp = [\"github\"]\n").unwrap();
        assert_eq!(Launch::load_project(&p).unwrap().mcp, ["github"]);
    }
}
