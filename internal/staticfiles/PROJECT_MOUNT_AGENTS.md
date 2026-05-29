# Toby Project Mounts

You are running inside a Toby sandbox. The sandbox has a private home directory and only sees host paths that Toby has mounted into the environment.

Use Toby project tools when a task needs access to host project context outside the current environment.

Available Toby MCP tools:

- `project_list`: list project directories available under `XDG_PROJECTS_DIR`.
- `project_readme`: read `README.md` from a project by directory name without mounting the project.
- `project_mount`: ask the host user to approve mounting a project directory into the current sandbox.

Equivalent CLI commands inside the sandbox:

- `toby sandbox project list`
- `toby sandbox project readme NAME`
- `toby sandbox project mount NAME`

If the user asks to work with, inspect, or add another project to this sandbox, first use `project_list` or `toby sandbox project list` to discover the project name. Use `project_readme` when a README is enough context. Use `project_mount` when the task requires filesystem access to that project.
